//go:build small_tests || all_tests

package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"gotest.tools/assert"
	is "gotest.tools/assert/cmp"
)

// Most of the methods on TestDynamoDbClient are unimplemented, because
// dynamodb_contract_test.go tests most of them.
//
// The exception to this is Scan(), which is the reason why the DynamoDbClient
// interface exists. Testing all the cases of the code that relies on Scan() is
// annoying, difficult, and/or nearly impossible without using this test double.
type TestDynamoDbClient struct {
	subscribers []dbAttributes
	scanSize    int
	scanCalls   int
	scanErr     error
}

func (client *TestDynamoDbClient) CreateTable(
	context.Context, *dynamodb.CreateTableInput, ...func(*dynamodb.Options),
) (_ *dynamodb.CreateTableOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) DescribeTable(
	context.Context,
	*dynamodb.DescribeTableInput,
	...func(*dynamodb.Options),
) (_ *dynamodb.DescribeTableOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) UpdateTimeToLive(
	context.Context,
	*dynamodb.UpdateTimeToLiveInput,
	...func(*dynamodb.Options),
) (_ *dynamodb.UpdateTimeToLiveOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) DeleteTable(
	context.Context, *dynamodb.DeleteTableInput, ...func(*dynamodb.Options),
) (_ *dynamodb.DeleteTableOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) GetItem(
	context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options),
) (_ *dynamodb.GetItemOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) PutItem(
	context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options),
) (_ *dynamodb.PutItemOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) DeleteItem(
	context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options),
) (_ *dynamodb.DeleteItemOutput, _ error) {
	return
}

func (client *TestDynamoDbClient) addSubscriberRecord(sub dbAttributes) {
	client.subscribers = append(client.subscribers, sub)
}

func (client *TestDynamoDbClient) addSubscribers(subs []*Subscriber) {
	for _, sub := range subs {
		subRec := newSubscriberRecord(sub)
		client.subscribers = append(client.subscribers, subRec)
	}
}

func (client *TestDynamoDbClient) Scan(
	_ context.Context, input *dynamodb.ScanInput, _ ...func(*dynamodb.Options),
) (output *dynamodb.ScanOutput, err error) {
	client.scanCalls++

	err = client.scanErr
	if err != nil {
		return
	}

	items := make([]dbAttributes, 0, len(client.subscribers))

	// Remember that our schema is to keep pending and verified subscribers
	// partitioned across disjoin Global Secondary Indexes.
	for _, sub := range client.subscribers {
		if _, ok := sub[*input.IndexName]; ok {
			items = append(items, sub)
		}
	}

	// Simulating pagination is a little tricky. We use the following functions
	// to trim the result set down to the scanSize after performing the full
	// scan. This is an in-memory test double, so it's fast enough.
	getEmail := func(attrs dbAttributes) (email string, err error) {
		return (&dbParser{attrs}).GetString("email")
	}
	startScan := func(items []dbAttributes) ([]dbAttributes, error) {
		var lastEmail string

		if lastItem := input.ExclusiveStartKey; len(lastItem) == 0 {
			return items, nil
		} else if lastEmail, err = getEmail(lastItem); err != nil {
			return items, nil
		}
		for i, sub := range items {
			var email string
			if email, err = getEmail(sub); err != nil {
				return nil, err
			} else if email == lastEmail {
				return items[i+1:], nil
			}
		}
		return items, nil
	}
	endScan := func(
		items []dbAttributes, n int,
	) (result []dbAttributes, lastKey dbAttributes, err error) {
		if n == 0 || len(items) <= n {
			result = items
			return
		}
		items = items[:n]

		if lastEmail, err := getEmail(items[len(items)-1]); err == nil {
			result = items
			lastKey = dbAttributes{"email": &dbString{Value: lastEmail}}
		}
		return
	}

	n := client.scanSize
	var lastKey dbAttributes

	if items, err = startScan(items); err != nil {
		return
	} else if items, lastKey, err = endScan(items, n); err == nil {
		output = &dynamodb.ScanOutput{Items: items, LastEvaluatedKey: lastKey}
	}
	return
}

func newSubscriberRecord(sub *Subscriber) dbAttributes {
	return dbAttributes{
		"email":            &dbString{Value: sub.Email},
		"uid":              &dbString{Value: sub.Uid.String()},
		string(sub.Status): toDynamoDbTimestamp(sub.Timestamp),
	}
}

func TestGetAttribute(t *testing.T) {
	attrs := dbAttributes{
		"email":      &dbString{Value: testEmail},
		"unexpected": &types.AttributeValueMemberBOOL{Value: false},
	}

	parseString := func(attr *dbString) (string, error) {
		return attr.Value, nil
	}

	t.Run("Succeeds", func(t *testing.T) {
		value, err := getAttribute("email", attrs, parseString)

		assert.NilError(t, err)
		assert.Equal(t, testEmail, value)
	})

	t.Run("ErrorsIfAttributeNotPresent", func(t *testing.T) {
		value, err := getAttribute("missing", attrs, parseString)

		assert.Equal(t, "", value)
		assert.ErrorContains(t, err, "attribute 'missing' not in: ")
	})

	t.Run("ErrorsIfNotExpectedAttributeType", func(t *testing.T) {
		value, err := getAttribute("unexpected", attrs, parseString)

		assert.Equal(t, "", value)
		errMsg := "attribute 'unexpected' is of type " +
			"*types.AttributeValueMemberBOOL, not "
		assert.ErrorContains(t, err, errMsg)
	})

	t.Run("ErrorsIfParsingFails", func(t *testing.T) {
		parseFail := func(attr *dbString) (string, error) {
			return "shouldn't see this", errors.New("parse failure")
		}

		value, err := getAttribute("email", attrs, parseFail)

		assert.Equal(t, "", value)
		assert.ErrorContains(t, err, "failed to parse 'email' from: ")
		assert.ErrorContains(t, err, ": parse failure")
	})
}

func TestParseSubscriber(t *testing.T) {
	t.Run("Succeeds", func(t *testing.T) {
		attrs := dbAttributes{
			"email":    &dbString{Value: testEmail},
			"uid":      &dbString{Value: testUid.String()},
			"verified": toDynamoDbTimestamp(testTimestamp),
		}

		subscriber, err := parseSubscriber(attrs)

		assert.NilError(t, err)
		assert.DeepEqual(t, subscriber, &Subscriber{
			testEmail, testUid, SubscriberVerified, testTimestamp,
		})
	})

	t.Run("ErrorsIfGettingAttributesFail", func(t *testing.T) {
		subscriber, err := parseSubscriber(dbAttributes{})

		assert.Check(t, is.Nil(subscriber))
		assert.ErrorContains(t, err, "failed to parse subscriber: ")
		assert.ErrorContains(t, err, "attribute 'email' not in: ")
		assert.ErrorContains(t, err, "attribute 'uid' not in: ")

		const errFmt = "has neither '%s' or '%s' attributes"
		expected := fmt.Sprintf(
			errFmt, SubscriberPending, SubscriberVerified,
		)
		assert.ErrorContains(t, err, expected)
	})

	t.Run("ErrorsIfContainsBothPendingAndVerified", func(t *testing.T) {
		attrs := dbAttributes{
			"email":    &dbString{Value: "foo@bar.com"},
			"uid":      &dbString{Value: testUid.String()},
			"pending":  toDynamoDbTimestamp(testTimestamp),
			"verified": toDynamoDbTimestamp(testTimestamp),
		}

		subscriber, err := parseSubscriber(attrs)

		assert.Check(t, is.Nil(subscriber))

		const errFmt = "contains both '%s' and '%s' attributes"
		expected := fmt.Sprintf(
			errFmt, SubscriberPending, SubscriberVerified,
		)
		assert.ErrorContains(t, err, expected)
	})

	t.Run("ErrorsIfTimestampIsNotAnInteger", func(t *testing.T) {
		attrs := dbAttributes{
			"email":    &dbString{Value: testEmail},
			"uid":      &dbString{Value: testUid.String()},
			"verified": &dbNumber{Value: "not an int"},
		}

		subscriber, err := parseSubscriber(attrs)

		assert.Check(t, is.Nil(subscriber))
		assert.ErrorContains(t, err, "failed to parse 'verified' from: ")
	})
}

const testStartKeyValue = "foo@bar.com"

var testStartKeyAttrs dbAttributes = dbAttributes{
	"primary": &dbString{Value: testStartKeyValue},
}
var testStartKey *dynamoDbStartKey = &dynamoDbStartKey{testStartKeyAttrs}

func TestDynamoDbStartKey(t *testing.T) {
	t.Run("IsDbStartKeyDoesNothingButMatchTheInterface", func(t *testing.T) {
		startKey := &dynamoDbStartKey{}

		startKey.isDbStartKey()
	})
}

type bogusDbStartKey struct{}

func (*bogusDbStartKey) isDbStartKey() {}

func TestNewScanInput(t *testing.T) {
	t.Run("Succeeds", func(t *testing.T) {
		t.Run("WithNilStartKey", func(t *testing.T) {
			input, err := newScanInput(
				"subscribers", SubscriberVerified, nil,
			)

			assert.NilError(t, err)
			assert.Equal(t, "subscribers", *input.TableName)
			assert.Equal(t, DynamoDbVerifiedIndexName, *input.IndexName)
			assert.Assert(t, is.Nil(input.ExclusiveStartKey))
		})

		t.Run("WithExistingStartKey", func(t *testing.T) {
			input, err := newScanInput(
				"subscribers", SubscriberPending, testStartKey,
			)

			assert.NilError(t, err)
			assert.Equal(t, "subscribers", *input.TableName)
			assert.Equal(t, DynamoDbPendingIndexName, *input.IndexName)
			assert.Assert(t, is.Contains(input.ExclusiveStartKey, "primary"))

			actualKey := input.ExclusiveStartKey["primary"].(*dbString)
			assert.Equal(t, testStartKeyValue, actualKey.Value)
		})
	})

	t.Run("ErrorsIfInvalidStartKey", func(t *testing.T) {
		input, err := newScanInput(
			"subscribers", SubscriberVerified, &bogusDbStartKey{},
		)

		assert.Assert(t, is.Nil(input))
		assert.Error(t, err, "not a *db.dynamoDbStartKey: *db.bogusDbStartKey")
	})
}

func TestProcessScanOutput(t *testing.T) {
	setup := func() *dynamodb.ScanOutput {
		return &dynamodb.ScanOutput{
			Items: []dbAttributes{
				newSubscriberRecord(testVerifiedSubscribers[0]),
				newSubscriberRecord(testVerifiedSubscribers[1]),
				newSubscriberRecord(testVerifiedSubscribers[2]),
			},
		}
	}

	getDbStartKey := func(t *testing.T, startKey StartKey) *dynamoDbStartKey {
		t.Helper()
		var dbStartKey *dynamoDbStartKey
		var ok bool

		if dbStartKey, ok = startKey.(*dynamoDbStartKey); !ok {
			t.Fatalf("start key is not *dynamoDbStartKey: %T", startKey)
		}
		return dbStartKey
	}

	checkDbStartKeyContains := func(
		t *testing.T, startKey *dynamoDbStartKey, key, value string,
	) {
		t.Helper()

		assert.Assert(t, is.Contains(startKey.attrs, key))
		actualKey := startKey.attrs[key].(*dbString)
		assert.Equal(t, value, actualKey.Value)
	}

	t.Run("Succeeds", func(t *testing.T) {
		expectedSubs := []*Subscriber{
			testVerifiedSubscribers[0],
			testVerifiedSubscribers[1],
			testVerifiedSubscribers[2],
		}

		t.Run("WithoutNextStartKey", func(t *testing.T) {
			output := setup()

			subs, nextStartKey, err := processScanOutput(output)

			assert.NilError(t, err)
			assert.Assert(t, is.Nil(nextStartKey))
			assert.DeepEqual(t, expectedSubs, subs)
		})

		t.Run("WithNextStartKey", func(t *testing.T) {
			output := setup()
			output.LastEvaluatedKey = testStartKey.attrs

			subs, nextStartKey, err := processScanOutput(output)

			assert.NilError(t, err)
			startKey := getDbStartKey(t, nextStartKey)
			checkDbStartKeyContains(t, startKey, "primary", testStartKeyValue)
			assert.DeepEqual(t, expectedSubs, subs)
		})
	})

	t.Run("ReturnsParseSubscriberErrors", func(t *testing.T) {
		output := setup()
		const statusKey string = string(SubscriberPending)
		for _, record := range output.Items {
			record[statusKey] = toDynamoDbTimestamp(testTimestamp)
		}

		subs, _, err := processScanOutput(output)

		assert.DeepEqual(t, []*Subscriber{nil, nil, nil}, subs)
		expectedErr := fmt.Sprintf(
			"failed to parse subscriber: "+
				"contains both '%s' and '%s' attributes",
			SubscriberPending,
			SubscriberVerified,
		)
		assert.ErrorContains(t, err, expectedErr)
	})
}

func setupDbWithSubscribers() (dyndb *DynamoDb, client *TestDynamoDbClient) {
	client = &TestDynamoDbClient{}
	dyndb = &DynamoDb{client, "subscribers-table"}

	client.addSubscribers(testPendingSubscribers)
	client.addSubscribers(testVerifiedSubscribers)
	return
}

func TestGetSubscribersInState(t *testing.T) {
	ctx := context.Background()

	t.Run("Succeeds", func(t *testing.T) {
		dyndb, _ := setupDbWithSubscribers()

		subs, next, err := dyndb.getSubscribersInState(
			ctx, SubscriberVerified, nil,
		)

		assert.NilError(t, err)
		assert.Assert(t, is.Nil(next))
		assert.DeepEqual(t, testVerifiedSubscribers, subs)
	})

	t.Run("FailsIfNewScanInputFails", func(t *testing.T) {
		dyndb, _ := setupDbWithSubscribers()

		subs, next, err := dyndb.getSubscribersInState(
			ctx, SubscriberVerified, &bogusDbStartKey{})

		assert.Assert(t, is.Nil(subs))
		assert.Assert(t, is.Nil(next))
		expectedErr := "failed to get verified subscribers: " +
			"not a *db.dynamoDbStartKey: *db.bogusDbStartKey"
		assert.Error(t, err, expectedErr)
	})

	t.Run("FailsIfScanFails", func(t *testing.T) {
		dyndb, client := setupDbWithSubscribers()
		client.scanErr = errors.New("scanning error")

		subs, next, err := dyndb.getSubscribersInState(
			ctx, SubscriberVerified, nil,
		)

		assert.Assert(t, is.Nil(subs))
		assert.Assert(t, is.Nil(next))
		expectedErr := "failed to get verified subscribers: scanning error"
		assert.ErrorContains(t, err, expectedErr)
	})

	t.Run("FailsIfProcessScanOutputFails", func(t *testing.T) {
		dyndb, client := setupDbWithSubscribers()
		status := SubscriberVerified
		client.addSubscriberRecord(dbAttributes{
			"email":        &dbString{Value: "bad-uid@foo.com"},
			"uid":          &dbString{Value: "not a uid"},
			string(status): toDynamoDbTimestamp(testTimestamp),
		})

		subs, _, err := dyndb.getSubscribersInState(
			ctx, SubscriberVerified, nil,
		)

		expectedSubscribers := append(testVerifiedSubscribers, nil)
		assert.DeepEqual(t, expectedSubscribers, subs)

		expectedErr := "failed to parse subscriber: " +
			"failed to parse 'uid' from: "
		assert.ErrorContains(t, err, expectedErr)
	})
}

func TestProcessSubscribersInState(t *testing.T) {
	ctx := context.Background()

	setup := func() (
		dyndb *DynamoDb,
		client *TestDynamoDbClient,
		subs *[]*Subscriber,
		f SubscriberFunc,
	) {
		dyndb, client = setupDbWithSubscribers()
		subs = &[]*Subscriber{}
		f = SubscriberFunc(func(s *Subscriber) bool {
			*subs = append(*subs, s)
			return true
		})
		return
	}

	t.Run("Succeeds", func(t *testing.T) {
		t.Run("WithoutPagination", func(t *testing.T) {
			dynDb, client, subs, f := setup()

			err := dynDb.ProcessSubscribersInState(ctx, SubscriberVerified, f)

			assert.NilError(t, err)
			assert.DeepEqual(t, testVerifiedSubscribers, *subs)
			assert.Equal(t, client.scanCalls, 1)
		})

		t.Run("WithPagination", func(t *testing.T) {
			dynDb, client, subs, f := setup()
			client.scanSize = 1

			err := dynDb.ProcessSubscribersInState(ctx, SubscriberVerified, f)

			assert.NilError(t, err)
			assert.DeepEqual(t, testVerifiedSubscribers, *subs)
			assert.Equal(t, client.scanCalls, len(testVerifiedSubscribers))
		})

		t.Run("WithoutProcessingAllSubscribers", func(t *testing.T) {
			dynDb, _, subs, _ := setup()
			f := SubscriberFunc(func(s *Subscriber) bool {
				*subs = append(*subs, s)
				return s.Email != testVerifiedSubscribers[1].Email
			})

			err := dynDb.ProcessSubscribersInState(ctx, SubscriberVerified, f)

			assert.NilError(t, err)
			assert.DeepEqual(t, testVerifiedSubscribers[:2], *subs)
		})
	})

	t.Run("ReturnsGetSubscribersError", func(t *testing.T) {
		dynDb, client, _, f := setup()
		client.scanErr = errors.New("scanning error")

		err := dynDb.ProcessSubscribersInState(ctx, SubscriberVerified, f)

		assert.ErrorContains(t, err, "scanning error")
	})
}
