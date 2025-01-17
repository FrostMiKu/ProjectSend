package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	_ "main/statik"

	"github.com/rakyll/statik/fs"
)

const ListenAtPort = 8042

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
	UrlV6     string `json:"urlV6"`
	AccessKey string `json:"ak"`
}

var globalDataLock sync.Mutex
var idCounter uint32
var manageKey string
var remoteAccessKey string
var manageUrl string
var msgList = make([]*MsgType, 0)

func sendAPIResponse(v interface{}, resp http.ResponseWriter) {
	buf, _ := json.Marshal(v)
	resp.Write(buf)
}

func afterMsgDeleted(m *MsgType) {
	if m == nil {
		return
	}
	if m.data != nil {
		eraseByteSlice(m.data)
	}
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

	// Download does not require a valid cookieAk.
	if path == "/api/download" {
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
		resp.Header().Add("Cache-control", "no-store")
		resp.Header().Add("Content-Disposition",
			fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, contentDispos, url.PathEscape(fileName), url.PathEscape(fileName)))
		contentType := getMimeTypeByFileName(fileName)
		resp.Header().Add("Content-Type", contentType)
		rdr := bytes.NewReader(fileData)
		http.ServeContent(resp, req, fileName, time.Now(), rdr)
		return
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
		fileSize, _ := strconv.ParseUint(urlQuery.Get("size"), 10, 36) // 64GB max (should be enough?)
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
	} else if path == "/api/delete" {
		id, _ := strconv.ParseUint(urlQuery.Get("id"), 10, 31)
		var msgToDelete *MsgType
		globalDataLock.Lock()
		for i, m := range msgList {
			if m.ID == uint32(id) {
				msgToDelete = m
				msgList = append(msgList[:i], msgList[i+1:]...)
				break
			}
		}
		globalDataLock.Unlock()
		if msgToDelete != nil {
			afterMsgDeleted(msgToDelete)
		}
		fmt.Fprintf(resp, retCodeTemplate, 0)
	} else if path == "/api/getAccessInfo" {
		ai := &AccessInfo{}
		ai.AccessKey = remoteAccessKey
		ai.Url = fmt.Sprintf("http://%s:%d/", getMyIPv4(), ListenAtPort)
		ai.UrlV6 = fmt.Sprintf("http://[%s]:%d/", getMyIPv6(), ListenAtPort)
		sendAPIResponse(&ApiResponse{0, ai}, resp)
	} else {
		if !canManage {
			fmt.Fprintf(resp, retCodeTemplate, -1)
			return
		}
	}
}

func main() {
	flagPtrStatic := flag.String("static", "", "Static resource path")
	flagPtrMk := flag.String("mk", "", "ManageKey")
	flag.Parse()

	manageKey = encodeBytesToHexString(genRandBytes(16))
	if *flagPtrMk != "" {
		manageKey = *flagPtrMk
	}
	remoteAccessKey = encodeBytesToHexString(genRandBytes(4))
	manageUrl = fmt.Sprintf("http://127.0.0.1:%d/?ak=%s", ListenAtPort, manageKey)

	fmt.Printf("ManageKey: %s\n", manageKey)
	fmt.Printf("ManageUrl: %s\n", manageUrl)

	serveMux := http.NewServeMux()

	statikFS, err := fs.New()
	if err != nil {
		log.Fatal(err)
	}
	if *flagPtrStatic != "" {
		serveMux.Handle("/", http.FileServer(http.Dir(*flagPtrStatic)))
	} else {
		serveMux.Handle("/", http.FileServer(statikFS))
	}
	serveMux.HandleFunc("/api/", handleAPI)

	httpServer := &http.Server{
		Addr:           fmt.Sprintf("0.0.0.0:%d", ListenAtPort),
		ReadTimeout:    1440 * time.Minute,
		WriteTimeout:   1440 * time.Minute,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
		Handler:        serveMux,
	}

	mime.AddExtensionType(".css", "text/css; charset=utf-8")
	mime.AddExtensionType(".html", "text/html; charset=utf-8")
	mime.AddExtensionType(".js", "application/javascript")

	go func() {
		time.Sleep(1 * time.Second)
		startBrowser(manageUrl)

	}()

	log.Fatal(httpServer.ListenAndServe())
}
