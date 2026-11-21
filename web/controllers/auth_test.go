package controllers

import (
	"strconv"
	"testing"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
)

func TestAPIKeyNeedsAConfiguredKey(t *testing.T) {
	const now = 1_700_000_000
	proof := func(key string, ts int) string { return crypt.Md5(key + strconv.Itoa(ts)) }

	// The bypass: no auth_key configured, so the "secret" is just the timestamp.
	if apiKeyValid("", proof("", now), now, now) {
		t.Fatal("an unset auth_key accepted md5(timestamp)")
	}
	if apiKeyValid("", "", now, now) {
		t.Fatal("an empty proof was accepted")
	}
	if !apiKeyValid("s3cret", proof("s3cret", now), now, now) {
		t.Fatal("the right proof was refused")
	}
	if apiKeyValid("s3cret", proof("other", now), now, now) {
		t.Fatal("a proof of another key was accepted")
	}
	if apiKeyValid("s3cret", proof("s3cret", now), now, now+21) {
		t.Fatal("a proof 21s old was accepted")
	}
	if !apiKeyValid("s3cret", proof("s3cret", now), now, now+20) {
		t.Fatal("a proof 20s old was refused")
	}
}

func TestAuthKeyServable(t *testing.T) {
	for _, c := range []struct {
		name, authKey, cryptKey string
		want                    bool
	}{
		{"nothing to hand out", "", "abcdefghijklmnop", false},
		{"shipped crypt key", "s3cret", common.DefaultAuthCryptKey, false},
		{"crypt key the wrong length", "s3cret", "short", false},
		{"configured", "s3cret", "abcdefghijklmnop", true},
	} {
		if got := authKeyServable(c.authKey, c.cryptKey); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
