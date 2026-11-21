package controllers

import (
	"encoding/hex"
	"time"

	"ehang.io/nps/lib/crypt"
	"github.com/astaxie/beego"
)

// defaultAuthCryptKey is the auth_crypt_key nps.conf ships with. It is public.
const defaultAuthCryptKey = "1234567812345678"

// authKeyServable says whether /auth/getauthkey may hand out the auth_key. It
// needs no login, so the encryption is all that protects the key: with an empty
// auth_key there is nothing to hand out, and with the shipped auth_crypt_key
// anyone can decrypt the answer.
func authKeyServable(authKey, cryptKey string) bool {
	return authKey != "" && len(cryptKey) == 16 && cryptKey != defaultAuthCryptKey
}

type AuthController struct {
	beego.Controller
}

func (s *AuthController) GetAuthKey() {
	m := make(map[string]interface{})
	defer func() {
		s.Data["json"] = m
		s.ServeJSON()
	}()
	if cryptKey := beego.AppConfig.String("auth_crypt_key"); !authKeyServable(beego.AppConfig.String("auth_key"), cryptKey) {
		m["status"] = 0
		return
	} else {
		b, err := crypt.AesEncrypt([]byte(beego.AppConfig.String("auth_key")), []byte(cryptKey))
		if err != nil {
			m["status"] = 0
			return
		}
		m["status"] = 1
		m["crypt_auth_key"] = hex.EncodeToString(b)
		m["crypt_type"] = "aes cbc"
		return
	}
}

func (s *AuthController) GetTime() {
	m := make(map[string]interface{})
	m["time"] = time.Now().Unix()
	s.Data["json"] = m
	s.ServeJSON()
}
