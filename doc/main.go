package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
	"fyne.io/systray"
	"strconv"
)

//go:embed icon.ico
var iconData []byte

const shinyAppURL = "https://tyler-powell.shinyapps.io/dst-crockpot/"
const dstProcessName = "dontstarve_steam_x64.exe" // confirm exact name via Task Manager

var myToken string

// =================== //
// Token Management  ----
// =================== //

func loadOrCreateToken() (string, bool) {
	exePath, err := os.Executable()
	if err != nil {
		exePath = "."
	}
	tokenPath := filepath.Join(filepath.Dir(exePath), "pairing_token.txt")

	data, err := os.ReadFile(tokenPath)
	if err == nil && len(data) > 0 {
		return string(data), false // existing token, not new
	}

	tokenBytes := make([]byte, 8)
	rand.Read(tokenBytes)
	newToken := hex.EncodeToString(tokenBytes)

	os.WriteFile(tokenPath, []byte(newToken), 0644)
	return newToken, true // freshly generated
}

func openBrowser(targetURL string) {
	err := exec.Command("cmd", "/c", "start", targetURL).Start()
	if err != nil {
		log.Println("Failed to open browser:", err)
	}
}

// =================== //
// HTTP Server        ----
// =================== //

func handleExport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	payload := string(body)
	log.Println("Received payload:", payload)

	targetURL := shinyAppURL + "?gamedata=" + url.QueryEscape(payload) + "&token=" + url.QueryEscape(myToken) + "&ts=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	resp, err := http.Get(targetURL)
	if err != nil {
		log.Println("Failed to forward to Shiny app:", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	log.Println("Forwarded to Shiny app, status:", resp.StatusCode)

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func startServer() {
	http.HandleFunc("/export", handleExport)
	log.Println("DST Helper listening on http://127.0.0.1:8000/export")
	log.Fatal(http.ListenAndServe("127.0.0.1:8000", nil))
}

// =================== //
// DST Process Watch ----
// =================== //

type processEntry32 struct {
	Size              uint32
	CntUsage          uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	CntThreads        uint32
	ParentProcessID   uint32
	PriorityClassBase int32
	Flags             uint32
	ExeFile           [260]uint16
}

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First           = kernel32.NewProc("Process32FirstW")
	procProcess32Next            = kernel32.NewProc("Process32NextW")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
)

func isDstRunning() bool {
	snapshot, _, _ := procCreateToolhelp32Snapshot.Call(0x00000002, 0) // TH32CS_SNAPPROCESS
	if snapshot == uintptr(syscall.InvalidHandle) {
		return true // fail safe: assume running rather than quit incorrectly
	}
	defer procCloseHandle.Call(snapshot)

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, dstProcessName) {
			return true
		}
		ret, _, _ = procProcess32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	return false
}

func watchDstProcess() {
	time.Sleep(10 * time.Second) // give DST a moment to actually be running before the first check
	for {
		if !isDstRunning() {
			log.Println("DST no longer running, shutting down helper.")
			systray.Quit()
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// =================== //
// Native Message Box ----
// =================== //

func showMessageBox(title, message string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	msgPtr, _ := syscall.UTF16PtrFromString(message)
	messageBox.Call(0, uintptr(unsafe.Pointer(msgPtr)), uintptr(unsafe.Pointer(titlePtr)), 0)
}

// =================== //
// Tray Icon          ----
// =================== //
func onExit() {
	os.Exit(0)
}
func onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("DST Crockpot Helper")
	systray.SetTooltip("Forwards DST inventory exports to your Crockpot Solver")

	mOpenApp := systray.AddMenuItem("Open Crockpot Solver", "Open the web app in your browser")
	mQuit := systray.AddMenuItem("Quit", "Stop the helper")

	pairedURL := shinyAppURL + "?token=" + url.QueryEscape(myToken)

	go startServer()
	go watchDstProcess()

	go func() {
		for {
			select {
			case <-mOpenApp.ClickedCh:
				openBrowser(pairedURL)
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// =================== //
// Main               ----
// =================== //

func main() {
	logFile, err := os.OpenFile("dst-helper.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	myToken, _ = loadOrCreateToken()
	systray.Run(onReady, onExit)
}
