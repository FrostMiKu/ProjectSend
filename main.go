package main

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MsgTypeText = 0
const MsgTypeFile = 1

type MsgType struct {
	ID        uint32 `json:"id"`
	Type      uint32 `json:"type"`
	CreatedAt int64  `json:"createdAt"`
	Text      string `json:"text"`
	Key       string `json:"key"`
	data      []byte
}

type ApiResponse struct {
	Ret  int         `json:"ret"`
	Data interface{} `json:"data"`
}

type AccessInfo struct {
	Url       string `json:"url"`
	AccessKey string `json:"ak"`
}

var globalDataLock sync.Mutex
var idCounter uint32
var manageKey string
var remoteAccessKey string
var manageUrl string
var remoteAccessUrl string
var msgList = make([]*MsgType, 0)
var mimeTypeTable = map[string]string{
	".pdf": "application/pdf",
}

func sendAPIResponse(v interface{}, resp http.ResponseWriter) {
	buf, _ := json.Marshal(v)
	resp.Write(buf)
}

func handleAPI(resp http.ResponseWriter, req *http.Request) {
	var canRemoteAccess = false
	var canManage = false
	var retCodeTemplate = `{"ret":%d}`

	path := req.URL.Path
	urlQuery := req.URL.Query()

	cookieAk, _ := req.Cookie("ak")
	if cookieAk != nil {
		if cookieAk.Value == manageKey {
			canManage = true
			canRemoteAccess = true
		} else if cookieAk.Value == remoteAccessKey {
			canRemoteAccess = true
		}
	}

	if !canRemoteAccess {
		fmt.Fprintf(resp, retCodeTemplate, -1)
		return
	}

	if path == "/api/getMsgList" {
		globalDataLock.Lock()
		sendAPIResponse(&ApiResponse{0, msgList}, resp)
		globalDataLock.Unlock()
	} else if path == "/api/addText" {
		req.ParseForm()
		idCounter++
		msg := &MsgType{}
		msg.Type = MsgTypeText
		msg.ID = idCounter
		msg.CreatedAt = time.Now().Unix()
		msg.Text = req.PostForm.Get("text")

		globalDataLock.Lock()
		msgList = append(msgList, msg)
		globalDataLock.Unlock()

		fmt.Fprintf(resp, retCodeTemplate, 0)
	} else if path == "/api/addFile" {
		fileName := urlQuery.Get("name")
		fileSize, _ := strconv.ParseUint(urlQuery.Get("size"), 10, 31)
		if (len(fileName) < 1) || (fileSize < 1) {
			fmt.Fprintf(resp, retCodeTemplate, -2)
			return
		}
		bodyBuf := make([]byte, fileSize)
		body := req.Body
		defer body.Close()
		realBytesRead, _ := io.ReadFull(body, bodyBuf)
		if realBytesRead != int(fileSize) {
			fmt.Fprintf(resp, retCodeTemplate, -3)
			return
		}
		idCounter++
		msg := &MsgType{}
		msg.ID = idCounter
		msg.Type = MsgTypeFile
		msg.CreatedAt = time.Now().Unix()
		msg.Text = fileName
		msg.Key = encodeBytesToHexString(genRandBytes(16))
		msg.data = bodyBuf

		globalDataLock.Lock()
		msgList = append(msgList, msg)
		globalDataLock.Unlock()

		fmt.Fprintf(resp, retCodeTemplate, 0)
	} else if path == "/api/download" {
		isPreview := false
		if urlQuery.Get("p") == "1" {
			isPreview = true
		}
		key := urlQuery.Get("k")
		if len(key) != 32 {
			return
		}
		globalDataLock.Lock()
		var fileData []byte
		var fileName string
		for _, msg := range msgList {
			if msg.Key == key {
				fileData = msg.data
				fileName = msg.Text
				break
			}
		}
		globalDataLock.Unlock()
		if fileData == nil {
			return
		}
		contentDispos := "attachment"
		if isPreview {
			contentDispos = "inline"
		}
		resp.Header().Add("Content-Disposition",
			fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, contentDispos, url.PathEscape(fileName), url.PathEscape(fileName)))
		contentType := "application/octet-stream"
		if isPreview {
			contentType = getMimeTypeByFileName(fileName)
		}
		resp.Header().Add("Content-Type", contentType)
		resp.Write(fileData)
	} else if path == "/api/getAccessInfo" {
		ai := &AccessInfo{}
		ai.AccessKey = remoteAccessKey
		ai.Url = fmt.Sprintf("http://%s:8042/", getMyIpAddress())
		sendAPIResponse(&ApiResponse{0, ai}, resp)
	} else {
		if !canManage {
			fmt.Fprintf(resp, retCodeTemplate, -1)
			return
		}
	}
}

// Get preferred outbound ip of this machine
func getMyIpAddress() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP
}

func main() {

	manageKey = encodeBytesToHexString(genRandBytes(16))
	remoteAccessKey = encodeBytesToHexString(genRandBytes(4))
	manageUrl = fmt.Sprintf("http://127.0.0.1:8042/?ak=%s", manageKey)
	remoteAccessUrl = fmt.Sprintf("http://%s:8042/?ak=%s", getMyIpAddress(), remoteAccessKey)

	fmt.Printf("ManageKey: %s\n", manageKey)
	fmt.Printf("ManageUrl: %s\n", manageUrl)
	startBrowser(manageUrl)

	serveMux := http.NewServeMux()
	serveMux.Handle("/", http.FileServer(http.Dir("static")))
	serveMux.HandleFunc("/api/", handleAPI)

	httpServer := &http.Server{
		Addr:           "0.0.0.0:8042",
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
		Handler:        serveMux,
	}

	log.Fatal(httpServer.ListenAndServe())
}

func encodeBytesToHexString(b []byte) string {
	return hex.EncodeToString(b)
}

func encodeBytesToBinaryString(b []byte) string {
	return string(b)
}

func genRandBytes(l int) []byte {
	ret := make([]byte, l)
	_, err := cryptoRand.Read(ret)
	if err != nil {
		return nil
	}
	return ret
}

func eraseByteSlice(b []byte) {
	if b == nil {
		return
	}
	l := len(b)
	for i := 0; i < l; i++ {
		b[i] = 0xcc
	}
}

func startBrowser(url string) error {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	return err
}

func getMimeTypeByFileName(fileName string) string {
	f := strings.ToLower(fileName)
	for k, v := range mimeTypeTable {
		if strings.HasSuffix(f, k) {
			return v
		}
	}
	return "application/octet-stream"
}