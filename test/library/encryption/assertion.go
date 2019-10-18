package encryption

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/stretchr/testify/require"
)

var protoEncodingPrefix = []byte{0x6b, 0x38, 0x73, 0x00}

const (
	jsonEncodingPrefix           = "{"
	protoEncryptedDataPrefix     = "k8s:enc:"
	aesCBCTransformerPrefixV1    = "k8s:enc:aescbc:v1:"
	secretboxTransformerPrefixV1 = "k8s:enc:secretbox:v1:"
)

func AssertSecretsAndConfigMaps(t testing.TB, etcdClient EtcdClient, expectedMode string) {
	t.Helper()
	require.NoError(t, AssertSecrets(t, etcdClient, expectedMode))
	require.NoError(t, AssertConfigMaps(t, etcdClient, expectedMode))
}

func AssertSecrets(t testing.TB, etcdClient EtcdClient, expectedMode string) error {
	t.Logf("Checking if all Secrets where encrypted/decrypted for %q mode", expectedMode)
	totalSecrets, err := verifyResources(t, etcdClient, "/kubernetes.io/secrets/", expectedMode)
	t.Logf("Verified %d Secrets, err %v", totalSecrets, err)
	return err
}

func AssertConfigMaps(t testing.TB, etcdClient EtcdClient, expectedMode string) error {
	t.Logf("Checking if all ConfigMaps where encrypted/decrypted for %q mode", expectedMode)
	totalConfigMaps, err := verifyResources(t, etcdClient, "/kubernetes.io/configmaps/", expectedMode)
	t.Logf("Verified %d ConfigMaps, err %v", totalConfigMaps, err)
	return err
}

func verifyResources(t testing.TB, etcdClient EtcdClient, etcdKeyPreifx, expectedMode string) (int, error) {
	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	resp, err := etcdClient.Get(timeout, etcdKeyPreifx, clientv3.WithPrefix())
	switch {
	case err != nil:
		return 0, fmt.Errorf("failed to list prefix %s: %v", etcdKeyPreifx, err)
	case resp.Count == 0 || len(resp.Kvs) == 0:
		return 0, fmt.Errorf("empty list response for prefix %s: %+v", etcdKeyPreifx, resp)
	case resp.More:
		return 0, fmt.Errorf("incomplete list response for prefix %s: %+v", etcdKeyPreifx, resp)
	}

	for _, keyValue := range resp.Kvs {
		if err := verifyPrefixForRawData(expectedMode, keyValue.Value); err != nil {
			return 0, fmt.Errorf("key %s failed check: %v\n%s", keyValue.Key, err, hex.Dump(keyValue.Value))
		}
	}

	return len(resp.Kvs), nil
}

func verifyPrefixForRawData(expectedMode string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty data")
	}

	conditionToStr := func(condition bool) string {
		if condition {
			return "encrypted"
		}
		return "unencrypted"
	}

	expectedEncrypted := true
	if expectedMode == "identity" {
		expectedMode = "identity-proto"
		expectedEncrypted = false
	}

	actualMode, isEncrypted := encryptionModeFromEtcdValue(data)
	if expectedEncrypted != isEncrypted {
		return fmt.Errorf("unexpected encrypted state, expected data to be %q but was %q with mode %q", conditionToStr(expectedEncrypted), conditionToStr(isEncrypted), actualMode)
	}
	if actualMode != expectedMode {
		return fmt.Errorf("unexpected encryption mode %q, expected %q, was data encrypted/decrypted with a wrong key", actualMode, expectedMode)
	}

	return nil
}

func encryptionModeFromEtcdValue(data []byte) (string, bool) {
	isEncrypted := bytes.HasPrefix(data, []byte(protoEncryptedDataPrefix)) // all encrypted data has this prefix
	return func() string {
		switch {
		case hasPrefixAndTrailingData(data, []byte(aesCBCTransformerPrefixV1)): // AES-CBC has this prefix
			return "aescbc"
		case hasPrefixAndTrailingData(data, []byte(secretboxTransformerPrefixV1)): // Secretbox has this prefix
			return "secretbox"
		case hasPrefixAndTrailingData(data, []byte(jsonEncodingPrefix)): // unencrypted json data has this prefix
			return "identity-json"
		case hasPrefixAndTrailingData(data, protoEncodingPrefix): // unencrypted protobuf data has this prefix
			return "identity-proto"
		default:
			return "unknown" // this should never happen
		}
	}(), isEncrypted
}

func hasPrefixAndTrailingData(data, prefix []byte) bool {
	return bytes.HasPrefix(data, prefix) && len(data) > len(prefix)
}