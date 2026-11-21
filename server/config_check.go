package server

import (
	"strings"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

// A ConfigWarning is a setting that leaves the server open to whoever finds it.
type ConfigWarning struct {
	Kind   string
	Detail string
}

// minPublicVkeyLen is how long a public vkey must be to be called anything but
// guessable, the same bar as the vkeys nps makes, which are 16 random characters.
const minPublicVkeyLen = 16

// guessable are the values that are tried first; nps itself shipped 123.
var guessable = map[string]bool{
	"123": true, "1234": true, "12345": true, "123456": true, "1234567": true,
	"12345678": true, "test": true, "password": true, "admin": true, "nps": true, "public": true,
}

// UnsafeConfig is what is wrong with the settings that get, which looks a
// setting up by name, returns, for an operator to read.
func UnsafeConfig(get func(string) string) []ConfigWarning {
	var out []ConfigWarning
	if v := get("public_vkey"); v != "" {
		if len(v) < minPublicVkeyLen || guessable[strings.ToLower(v)] {
			out = append(out, ConfigWarning{"public-vkey", "public_vkey is set and can be guessed: anyone who reaches the bridge port with it can create clients of their own and open tunnels on this server's ports. " +
				"Empty it in nps.conf unless a client needs it, and if one does, use 16 or more random characters."})
		} else {
			out = append(out, ConfigWarning{"public-vkey", "public_vkey is set: anyone who has it can connect, create clients and open tunnels on this server. Empty it in nps.conf unless a client needs it."})
		}
	}
	if k := get("auth_key"); k != "" {
		if len(k) < minPublicVkeyLen || guessable[strings.ToLower(k)] {
			out = append(out, ConfigWarning{"auth-key", "auth_key is set and can be guessed: it logs anyone who has it in to the web API as admin. Use 16 or more random characters, or empty it in nps.conf."})
		}
		if get("auth_crypt_key") == common.DefaultAuthCryptKey {
			out = append(out, ConfigWarning{"auth-crypt-key", "auth_crypt_key is still the value nps.conf ships with, so /auth/getauthkey is switched off: it would hand the auth_key to anyone, who could decrypt it with that public key. Set auth_crypt_key to 16 random characters to turn it back on."})
		}
	}
	user, pass := get("web_username"), get("web_password")
	if (user == "admin" && pass == "123") || pass == "" {
		out = append(out, ConfigWarning{"default-web-login", "the web panel has the login it shipped with, or none. It manages every client, tunnel and update, so set web_username and web_password in nps.conf, " +
			"and keep the panel off the public internet: web_ip=127.0.0.1 and an SSH tunnel, or a firewall rule for your own address."})
	}
	return out
}

// WarnUnsafeConfig says at start, in the log and among the alerts, what is
// unsafe in the configuration.
func WarnUnsafeConfig() {
	for _, w := range UnsafeConfig(beego.AppConfig.String) {
		logs.Warn("[SECURITY] %s", w.Detail)
		bridge.Raise(w.Kind, w.Detail)
	}
}
