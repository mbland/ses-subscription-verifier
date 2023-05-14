package email

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"

	"github.com/mbland/elistman/ops"
)

// AddressValidator wraps the ValidateAddress method.
//
// ValidateAddress parses and validates email addresses. The intent is to reduce
// bounced emails and other potential abuse by filtering out bad addresses
// before attempting to send email to them.
//
// The failure return value will be nil if the address passes validation, or non
// nil if it fails.
type AddressValidator interface {
	ValidateAddress(
		ctx context.Context, email string,
	) (failure *ValidationFailure, err error)
}

type ValidationFailure struct {
	Reason string
}

func (vf *ValidationFailure) String() string {
	return vf.Reason
}

// Resolver wraps several methods from the net standard library.
//
// This interface allows for unit testing code that relies on these methods
// without performing actual DNS lookups.
//
// See [net] for documentation on these methods.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) (addrs []string, err error)
	LookupAddr(ctx context.Context, addr string) (names []string, err error)
}

// ProdAddressValidator is the production implementation of AddressValidator.
type ProdAddressValidator struct {
	Suppressor Suppressor
	Resolver   Resolver
}

// ValidateAddress parses and validates email addresses.
//
// The return value will be nil if the address passes validation, or non nil if
// it fails.
//
// This method:
//
//   - Parses the username and domain with the help of [mail.ParseAddress]
//   - Rejects known invalid usernames and domains
//   - Rejects addresses on the Simple Email Service account-level suppression
//     list
//   - Looks up the DNS MX records (mail hosts) for the domain
//   - Confirms that at least one mail host is valid by examining DNS records
//
// The mail host validation happens by iterating over each MX record until one
// satisfies the following series of checks:
//
//   - Resolve the MX record's hostname to an IP address
//   - Resolve the IP address to a hostname via reverse DNS lookup (depends on a
//     DNS PTR record)
//   - Resolve that hostname to an IP address
//   - Check that two IP addresses match
//
// Each of the lookups above can produce one or more addresses or hostnames.
// ValidateAddress will iterate through every one until a match is found, or
// return an error describing all failed attempts to find a match.
//
// This algorithm was inspired by the "Reverse Entries for MX records" check
// from [DNS Inspect].
//
// Originally ValidateAddress was to implement the algorithm from [How to Verify
// Email Address Without Sending an Email].  The idea is to confirm the username
// exists for the email address domain before actually sending to it. However,
// most mail hosts these days don't allow anyone to dial in and ping mailboxes
// anymore, rendering this algorithm ineffective.
//
// DNS validation of the domain and bounce notification handling in
// [github.com/mbland/elistman/handler.Handler.HandleEvent] should minimize
// the risk of bounces and abuse.
//
// [DNS Inspect]: https://dnsinspect.com/
// [How to Verify Email Address Without Sending an Email]: https://mailtrap.io/blog/verify-email-address-without-sending/
func (av *ProdAddressValidator) ValidateAddress(
	ctx context.Context, address string,
) (failure *ValidationFailure, err error) {
	var result bool
	email, user, domain, err := parseAddress(address)

	if err != nil {
		failure = &ValidationFailure{"address failed to parse: " + address}
		err = nil
	} else if isKnownInvalidAddress(user, domain) {
		failure = &ValidationFailure{"invalid email address: " + address}
	} else if result, err = av.Suppressor.IsSuppressed(ctx, email); err != nil {
		return
	} else if result {
		failure = &ValidationFailure{"suppressed email address: " + address}
	} else if err = av.checkMailHosts(ctx, email, domain); err != nil {
		failure = &ValidationFailure{
			fmt.Sprintf("address failed DNS validation: %s: %s", address, err),
		}
		err = nil
	}
	return
}

func parseAddress(address string) (email, user, domain string, err error) {
	addr, parseErr := mail.ParseAddress(address)

	if parseErr != nil {
		err = fmt.Errorf("invalid email address: %s: %s", address, parseErr)
	} else {
		email = addr.Address
		// mail.ParseAddress guarantees an "@domain" part is present.
		i := strings.LastIndexByte(email, '@')
		user = email[0:i]
		domain = email[i+1:]
	}
	return
}

var invalidUserNames = map[string]bool{
	"postmaster": true,
	"abuse":      true,
}

var invalidDomains = map[string]bool{
	"localhost":   true,
	"example.com": true,
}

func isKnownInvalidAddress(user, domain string) bool {
	return invalidUserNames[strings.Split(user, "+")[0]] ||
		strings.HasPrefix(domain, "[") ||
		net.ParseIP(domain) != nil ||
		invalidDomains[domain] ||
		invalidDomains[getPrimaryDomain(domain)]
}

func getPrimaryDomain(domainName string) string {
	parts := strings.Split(domainName, ".")
	return strings.Join(parts[len(parts)-2:], ".")
}

func (av *ProdAddressValidator) checkMailHosts(
	ctx context.Context, email, domain string,
) error {
	mxRecords, err := lookup(av.Resolver.LookupMX, ctx, domain)

	// If LookupMX failed to resolve any hosts, it could be due to a typo. In
	// this case, don't add the address to the suppression list.
	if len(mxRecords) == 0 {
		const errFmt = "failed to retrieve MX records for %s: %w"
		return fmt.Errorf(errFmt, domain, err)
	}

	errs := make([]error, len(mxRecords))

	for i, record := range mxRecords {
		errs[i] = av.checkMailHost(ctx, record.Host)
		if errs[i] == nil {
			// Found a good MX host.
			return nil
		}
	}

	const errFmt = "no valid MX hosts for %s: %w"
	err = fmt.Errorf(errFmt, domain, errors.Join(errs...))

	// If LookupMX succeeded, but validating all the MX records fail, sending a
	// message to the address would bounce, so suppress the address. This will
	// short circuit ValidateAddress before it calls this method for the same
	// address.
	//
	// This could be a configuration or network issue, but it could also be an
	// attack. Of course, an attacker could use different addresses from the
	// same domain. It might be worth creating a table of suppressed domains at
	// some point.
	//
	// If it is a network issue, suppression will probably fail as well, so we
	// likely won't accidentally suppress anyone.
	suppressionErr := av.Suppressor.Suppress(ctx, email)
	return errors.Join(err, suppressionErr)
}

func (av *ProdAddressValidator) checkMailHost(
	ctx context.Context, mailHost string,
) error {
	mailHostIps, err := lookup(av.Resolver.LookupHost, ctx, mailHost)

	if err != nil {
		return err
	}

	errs := make([]error, len(mailHostIps))

	for i, mailIp := range mailHostIps {
		errs[i] = av.checkReverseLookupHostResolvesToOriginalIp(ctx, mailIp)
		if errs[i] == nil {
			return nil
		}
	}

	const errFmt = "reverse lookup of addresses for %s failed: %w"
	return fmt.Errorf(errFmt, mailHost, errors.Join(errs...))
}

func (av *ProdAddressValidator) checkReverseLookupHostResolvesToOriginalIp(
	ctx context.Context, addr string,
) error {
	hosts, err := lookup(av.Resolver.LookupAddr, ctx, addr)

	if err != nil {
		return err
	}
	errs := make([]error, len(hosts))

	for i, host := range hosts {
		errs[i] = av.checkHostResolvesToAddress(ctx, host, addr)
		if errs[i] == nil {
			return nil
		}
	}

	const errFmt = "no host resolves to %s: %w"
	return fmt.Errorf(errFmt, addr, errors.Join(errs...))
}

func (av *ProdAddressValidator) checkHostResolvesToAddress(
	ctx context.Context, host, addr string,
) error {
	addrs, err := lookup(av.Resolver.LookupHost, ctx, host)

	if err != nil {
		return err
	}

	for _, hostAddr := range addrs {
		if hostAddr == addr {
			return nil
		}
	}
	return fmt.Errorf("%s resolves to %s", host, strings.Join(addrs, ", "))
}

// lookup calls a net.Resolver method and processes its errors.
//
// Specifically, it differentiates successful DNS responses that return no
// records from external errors, be they DNS configuration errors or networking
// errors:
//
//   - It returns nil if the error is a DNSError and IsNotFound is true.
//   - Otherwise it presumes the error is a network or other external failure
//     and wraps it with ops.ErrExternal.
//
// This relies on the following facts about net.Resolver:
//
//   - All errors are of type net.DNSError.
//   - If a host is found, it will be returned, even if there's a non-nil error
//     accompanying it in some cases.
//   - If there were no problems with the network or DNS response, but the host
//     was not found, no hosts are returned. The error will be a net.DNSError
//     value with IsNotFound == true.
//   - If there were network or DNS issues, no hosts are returned, and the error
//     will be a net.DNSError value with IsNotFound == false.
//
// Both [net.Resolver.LookupMX] and [net.Resolver.LookupAddr] can potentially
// return valid results and non nil error values. This is because both will
// filter returned DNS records, returning all valid records while reporting that
// malformed records exist. As a result, this function will pass through any
// returned records and return a nil error.
//
// [net.Resolver.LookupHost] doesn't explicitly state that it could return both
// valid records and a non nil error. However, wrapping it with [lookup] will do
// the right thing regardless.
func lookup[T []string | []*net.MX, F func(context.Context, string) (T, error)](
	lookup F, ctx context.Context, target string,
) (values T, err error) {
	values, err = lookup(ctx, target)
	var dnsErr *net.DNSError

	if len(values) != 0 {
		err = nil
	} else if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		err = fmt.Errorf("no records for %s", target)
	} else {
		const errFmt = "%w: failed to resolve %s: %w"
		err = fmt.Errorf(errFmt, ops.ErrExternal, target, err)
	}
	return
}
