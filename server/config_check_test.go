package server

import (
	"strings"
	"testing"
)

func settings(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func kinds(ws []ConfigWarning) string {
	var k []string
	for _, w := range ws {
		k = append(k, w.Kind)
	}
	return strings.Join(k, ",")
}

func TestUnsafeConfig(t *testing.T) {
	for _, c := range []struct {
		name string
		kv   map[string]string
		want string
	}{
		{"what nps used to ship", map[string]string{"public_vkey": "123", "web_username": "admin", "web_password": "123"}, "public-vkey,default-web-login"},
		{"nothing wrong", map[string]string{"public_vkey": "", "web_username": "ops", "web_password": "a long password that is not the default"}, ""},
		{"public key unset, web login default", map[string]string{"web_username": "admin", "web_password": "123"}, "default-web-login"},
		{"a long random public key is still said, more mildly", map[string]string{"public_vkey": "k3JqW9xL2mZpT7vB", "web_username": "ops", "web_password": "x-long-and-unlike-the-default"}, "public-vkey"},
		{"a short public key", map[string]string{"public_vkey": "abc", "web_username": "ops", "web_password": "x-long-and-unlike-the-default"}, "public-vkey"},
		{"a long public key that is a known one", map[string]string{"public_vkey": "PASSWORD", "web_username": "ops", "web_password": "x-long-and-unlike-the-default"}, "public-vkey"},
		{"no web password at all", map[string]string{"web_username": "ops", "web_password": ""}, "default-web-login"},
		{"a changed username with the default password is not the shipped login", map[string]string{"web_username": "ops", "web_password": "123"}, ""},
	} {
		if got := kinds(UnsafeConfig(settings(c.kv))); got != c.want {
			t.Errorf("%s: warned about %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTheGuessableAndTheMildWordingDiffer(t *testing.T) {
	weak := UnsafeConfig(settings(map[string]string{"public_vkey": "123", "web_username": "ops", "web_password": "long enough here"}))
	strong := UnsafeConfig(settings(map[string]string{"public_vkey": "k3JqW9xL2mZpT7vB", "web_username": "ops", "web_password": "long enough here"}))
	if len(weak) != 1 || len(strong) != 1 {
		t.Fatalf("%d and %d warnings", len(weak), len(strong))
	}
	if !strings.Contains(weak[0].Detail, "can be guessed") || strings.Contains(strong[0].Detail, "can be guessed") {
		t.Errorf("weak: %q\nstrong: %q", weak[0].Detail, strong[0].Detail)
	}
}
