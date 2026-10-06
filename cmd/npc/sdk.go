package main

import (
	"C"
	"os"
	"ehang.io/nps/client"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego/logs"
)

var cl *client.TRPClient

//export StartClientByVerifyKey
// The fingerprint of the server is taken from NPC_SERVER_FP, so that the
// signature callers were built against stays the one they get.
func StartClientByVerifyKey(serverAddr, verifyKey, connType, proxyUrl *C.char) int {
	_ = logs.SetLogger("store")
	if cl != nil {
		cl.Close()
	}
	cl = client.NewRPClient(C.GoString(serverAddr), C.GoString(verifyKey), C.GoString(connType), C.GoString(proxyUrl), os.Getenv("NPC_SERVER_FP"), nil, 60)
	cl.Start()
	return 1
}

//export GetClientStatus
func GetClientStatus() int {
	return client.NowStatus
}

//export CloseClient
func CloseClient() {
	if cl != nil {
		cl.Close()
	}
}

//export Version
func Version() *C.char {
	return C.CString(version.VERSION)
}

//export Logs
func Logs() *C.char {
	return C.CString(common.GetLogMsg())
}

func main() {
	// Need a main function to make CGO compile package as C shared library
}
