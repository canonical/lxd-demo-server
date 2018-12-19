package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/dustinkirkland/golang-petname"
	"github.com/gorilla/websocket"
	"github.com/lxc/lxd/client"
	lxdconfig "github.com/lxc/lxd/lxc/config"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/api"
	"github.com/pborman/uuid"
)

type Feedback struct {
	Rating   int    `json:"rating"`
	Email    string `json:"email"`
	EmailUse int    `json:"email_use"`
	Message  string `json:"message"`
}

func restFeedbackHandler(w http.ResponseWriter, r *http.Request) {
	if !config.Feedback {
		http.Error(w, "Feedback reporting is disabled", 400)
		return
	}

	if r.Method == "POST" {
		restFeedbackPostHandler(w, r)
		return
	}

	if r.Method == "GET" {
		restFeedbackGetHandler(w, r)
		return
	}

	if r.Method == "OPTIONS" {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}

		return
	}

	http.Error(w, "Not implemented", 501)
}

func restFeedbackPostHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Get the id argument
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "Missing session id", 400)
		return
	}

	// Get the container
	sessionId, _, _, _, _, sessionExpiry, err := dbGetContainer(id, false)
	if err != nil || sessionId == -1 {
		http.Error(w, "Session not found", 404)
		return
	}

	// Check if we can still store feedback
	if time.Now().Unix() > sessionExpiry+int64(config.FeedbackTimeout*60) {
		http.Error(w, "Feedback timeout has been reached", 400)
		return
	}

	// Parse request
	feedback := Feedback{}

	err = json.NewDecoder(r.Body).Decode(&feedback)
	if err != nil {
		http.Error(w, "Invalid JSON data", 400)
		return
	}

	err = dbRecordFeedback(sessionId, feedback)
	if err != nil {
		http.Error(w, "Unable to record feedback data", 500)
		return
	}

	return
}

func restFeedbackGetHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Get the id argument
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "Missing session id", 400)
		return
	}

	// Get the container
	sessionId, _, _, _, _, _, err := dbGetContainer(id, false)
	if err != nil || sessionId == -1 {
		http.Error(w, "Session not found", 404)
		return
	}

	// Get the feedback
	feedbackId, feedbackRating, feedbackEmail, feedbackEmailUse, feedbackComment, err := dbGetFeedback(sessionId)
	if err != nil || feedbackId == -1 {
		http.Error(w, "No existing feedback", 404)
		return
	}

	// Generate the response
	body := make(map[string]interface{})
	body["rating"] = feedbackRating
	body["email"] = feedbackEmail
	body["email_use"] = feedbackEmailUse
	body["feedback"] = feedbackComment

	// Return to the client
	err = json.NewEncoder(w).Encode(body)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}

	return
}

func restStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not implemented", 501)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	var failure bool

	// Parse the remote client information
	address, protocol, err := restClientIP(r)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}

	// Get some container data
	var containersCount int
	var containersNext int

	containersCount, err = dbActiveCount()
	if err != nil {
		failure = true
	}

	if containersCount >= config.ServerContainersMax {
		containersNext, err = dbNextExpire()
		if err != nil {
			failure = true
		}
	}

	// Generate the response
	body := make(map[string]interface{})
	body["client_address"] = address
	body["client_protocol"] = protocol
	body["feedback"] = config.Feedback
	body["server_console_only"] = config.ServerConsoleOnly
	body["server_ipv6_only"] = config.ServerIPv6Only
	if !config.ServerMaintenance && !failure {
		body["server_status"] = serverOperational
	} else {
		body["server_status"] = serverMaintenance
	}
	body["containers_count"] = containersCount
	body["containers_max"] = config.ServerContainersMax
	body["containers_next"] = containersNext

	err = json.NewEncoder(w).Encode(body)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
}

func restStatisticsHandler(w http.ResponseWriter, r *http.Request) {
	var err error

	if r.Method != "GET" {
		http.Error(w, "Not implemented", 501)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Validate API key
	requestKey := r.FormValue("key")
	if !shared.StringInSlice(requestKey, config.ServerStatisticsKeys) {
		http.Error(w, "Invalid authentication key", 401)
		return
	}

	// Unique host filtering
	statsUnique := false
	requestUnique := r.FormValue("unique")
	if shared.IsTrue(requestUnique) {
		statsUnique = true
	}

	// Time period filtering
	requestPeriod := r.FormValue("period")
	if !shared.StringInSlice(requestPeriod, []string{"", "total", "current", "hour", "day", "week", "month", "year"}) {
		http.Error(w, "Invalid period", 400)
		return
	}

	statsPeriod := requestPeriod

	if statsPeriod == "" {
		statsPeriod = "total"
	}

	// Network filtering
	requestNetwork := r.FormValue("network")
	var statsNetwork *net.IPNet
	if requestNetwork != "" {
		_, statsNetwork, err = net.ParseCIDR(requestNetwork)
		if err != nil {
			http.Error(w, "Invalid network", 400)
			return
		}
	}

	// Query the database
	count, err := dbGetStats(statsPeriod, statsUnique, statsNetwork)
	if err != nil {
		http.Error(w, "Unable to retrieve statistics", 500)
		return
	}

	// Return to client
	w.Write([]byte(fmt.Sprintf("%d\n", count)))
}

func restTermsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not implemented", 501)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Generate the response
	body := make(map[string]interface{})
	body["hash"] = config.serverTermsHash
	body["terms"] = config.ServerTerms

	err := json.NewEncoder(w).Encode(body)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
}

func restStartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not implemented", 501)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	body := make(map[string]interface{})
	requestDate := time.Now().Unix()

	// Extract IP
	requestIP, _, err := restClientIP(r)
	if err != nil {
		restStartError(w, err, containerUnknownError)
		return
	}

	// Check Terms of Service
	requestTerms := r.FormValue("terms")
	if requestTerms == "" {
		http.Error(w, "Missing terms hash", 400)
		return
	}

	if requestTerms != config.serverTermsHash {
		restStartError(w, nil, containerInvalidTerms)
		return
	}

	// Check for banned users
	if shared.StringInSlice(requestIP, config.ServerBannedIPs) {
		restStartError(w, nil, containerUserBanned)
		return
	}

	// Count running containers
	containersCount, err := dbActiveCount()
	if err != nil {
		containersCount = config.ServerContainersMax
	}

	// Server is full
	if containersCount >= config.ServerContainersMax {
		restStartError(w, nil, containerServerFull)
		return
	}

	// Count container for requestor IP
	containersCount, err = dbActiveCountForIP(requestIP)
	if err != nil {
		containersCount = config.QuotaSessions
	}

	if config.QuotaSessions != 0 && containersCount >= config.QuotaSessions {
		restStartError(w, nil, containerQuotaReached)
		return
	}

	// Create the container
	containerName := fmt.Sprintf("tryit-%s", petname.Adjective())
	containerUsername := petname.Adjective()
	containerPassword := petname.Adjective()
	id := uuid.NewRandom().String()

	// Config
	ctConfig := map[string]string{}

	ctConfig["security.nesting"] = "true"
	if config.QuotaCPU > 0 {
		ctConfig["limits.cpu"] = fmt.Sprintf("%d", config.QuotaCPU)
	}

	if config.QuotaRAM > 0 {
		ctConfig["limits.memory"] = fmt.Sprintf("%dMB", config.QuotaRAM)
	}

	if config.QuotaProcesses > 0 {
		ctConfig["limits.processes"] = fmt.Sprintf("%d", config.QuotaProcesses)
	}

	if !config.ServerConsoleOnly {
		ctConfig["user.user-data"] = fmt.Sprintf(`#cloud-config
ssh_pwauth: True
manage_etc_hosts: True
users:
 - name: %s
   groups: sudo
   plain_text_passwd: %s
   lock_passwd: False
   shell: /bin/bash
`, containerUsername, containerPassword)
	}

	var rop lxd.RemoteOperation
	if config.Container != "" {
		args := lxd.ContainerCopyArgs{
			Name:          containerName,
			ContainerOnly: true,
		}

		source, _, err := lxdDaemon.GetContainer(config.Container)
		if err != nil {
			restStartError(w, err, containerUnknownError)
			return
		}

		source.Config = ctConfig
		source.Profiles = config.Profiles

		rop, err = lxdDaemon.CopyContainer(lxdDaemon, *source, &args)
		if err != nil {
			restStartError(w, err, containerUnknownError)
			return
		}
	} else {
		defaultConfig := lxdconfig.DefaultConfig

		remote, fingerprint, err := defaultConfig.ParseRemote(config.Image)
		if err != nil {
			restStartError(w, err, containerUnknownError)
			return
		}

		var d lxd.ImageServer

		if remote == "local" {
			d = lxdDaemon
		} else {
			d, err = defaultConfig.GetImageServer(remote)
			if err != nil {
				restStartError(w, err, containerUnknownError)
				return
			}
		}

		if fingerprint == "" {
			fingerprint = "default"
		}

		alias, _, err := d.GetImageAlias(fingerprint)
		if err == nil {
			fingerprint = alias.Target
		}

		imgInfo, _, err := d.GetImage(fingerprint)
		if err != nil {
			restStartError(w, err, containerUnknownError)
			return
		}

		req := api.ContainersPost{
			Name: containerName,
		}
		req.Config = ctConfig
		req.Profiles = config.Profiles

		rop, err = lxdDaemon.CreateContainerFromImage(d, *imgInfo, req)
		if err != nil {
			restStartError(w, err, containerUnknownError)
			return
		}
	}

	err = rop.Wait()
	if err != nil {
		restStartError(w, err, containerUnknownError)
		return
	}

	// Configure the container devices
	ct, etag, err := lxdDaemon.GetContainer(containerName)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	if config.QuotaDisk > 0 {
		_, ok := ct.ExpandedDevices["root"]
		if ok {
			ct.Devices["root"] = ct.ExpandedDevices["root"]
			ct.Devices["root"]["size"] = fmt.Sprintf("%dGB", config.QuotaDisk)
		} else {
			ct.Devices["root"] = map[string]string{"type": "disk", "path": "/", "size": fmt.Sprintf("%dGB", config.QuotaDisk)}
		}
	}

	op, err := lxdDaemon.UpdateContainer(containerName, ct.Writable(), etag)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	err = op.Wait()
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	// Start the container
	req := api.ContainerStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err = lxdDaemon.UpdateContainerState(containerName, req, "")
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	err = op.Wait()
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	// Get the IP (30s timeout)
	var containerIP string
	if !config.ServerConsoleOnly {
		time.Sleep(2 * time.Second)
		timeout := 30
		for timeout != 0 {
			timeout--
			ct, _, err := lxdDaemon.GetContainerState(containerName)
			if err != nil {
				lxdForceDelete(lxdDaemon, containerName)
				restStartError(w, err, containerUnknownError)
				return
			}

			for netName, net := range ct.Network {
				if !shared.StringInSlice(netName, []string{"eth0", "lxcbr0"}) {
					continue
				}

				for _, addr := range net.Addresses {
					if addr.Address == "" {
						continue
					}

					if addr.Scope != "global" {
						continue
					}

					if config.ServerIPv6Only && addr.Family != "inet6" {
						continue
					}

					containerIP = addr.Address
					break
				}

				if containerIP != "" {
					break
				}
			}

			if containerIP != "" {
				break
			}

			time.Sleep(500 * time.Millisecond)
		}
	} else {
		containerIP = "console-only"
	}

	containerExpiry := time.Now().Unix() + int64(config.QuotaTime)

	if !config.ServerConsoleOnly {
		body["ip"] = containerIP
		body["username"] = containerUsername
		body["password"] = containerPassword
		body["fqdn"] = fmt.Sprintf("%s.lxd", containerName)
	}
	body["id"] = id
	body["expiry"] = containerExpiry

	// Setup cleanup code
	duration, err := time.ParseDuration(fmt.Sprintf("%ds", config.QuotaTime))
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	containerID, err := dbNew(id, containerName, containerIP, containerUsername, containerPassword, containerExpiry, requestDate, requestIP, requestTerms)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	time.AfterFunc(duration, func() {
		lxdForceDelete(lxdDaemon, containerName)
		dbExpire(containerID)
	})

	// Return to the client
	body["status"] = containerStarted
	err = json.NewEncoder(w).Encode(body)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		http.Error(w, "Internal server error", 500)
		return
	}
}

func restInfoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not implemented", 501)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Get the id
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "Missing session id", 400)
		return
	}

	// Get the container
	sessionId, containerName, containerIP, containerUsername, containerPassword, containerExpiry, err := dbGetContainer(id, false)
	if err != nil || sessionId == -1 {
		http.Error(w, "Session not found", 404)
		return
	}

	body := make(map[string]interface{})

	if !config.ServerConsoleOnly {
		body["ip"] = containerIP
		body["username"] = containerUsername
		body["password"] = containerPassword
		body["fqdn"] = fmt.Sprintf("%s.lxd", containerName)
	}
	body["id"] = id
	body["expiry"] = containerExpiry

	// Return to the client
	body["status"] = containerStarted
	err = json.NewEncoder(w).Encode(body)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		http.Error(w, "Internal server error", 500)
		return
	}
}

func restStartError(w http.ResponseWriter, err error, code statusCode) {
	body := make(map[string]interface{})
	body["status"] = code

	if err != nil {
		fmt.Printf("error: %s\n", err)
	}

	err = json.NewEncoder(w).Encode(body)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
}

func restClientIP(r *http.Request) (string, string, error) {
	var address string
	var protocol string

	viaProxy := r.Header.Get("X-Forwarded-For")

	if viaProxy != "" {
		address = viaProxy
	} else {
		host, _, err := net.SplitHostPort(r.RemoteAddr)

		if err == nil {
			address = host
		} else {
			address = r.RemoteAddr
		}
	}

	ip := net.ParseIP(address)
	if ip == nil {
		return "", "", fmt.Errorf("Invalid address: %s", address)
	}

	if ip.To4() == nil {
		protocol = "IPv6"
	} else {
		protocol = "IPv4"
	}

	return address, protocol, nil
}

func restConsoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not implemented", 501)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get the id argument
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "Missing session id", 400)
		return
	}

	// Get the container
	sessionId, containerName, _, _, _, _, err := dbGetContainer(id, true)
	if err != nil || sessionId == -1 {
		http.Error(w, "Session not found", 404)
		return
	}

	// Get console width and height
	width := r.FormValue("width")
	height := r.FormValue("height")

	if width == "" {
		width = "150"
	}

	if height == "" {
		height = "20"
	}

	widthInt, err := strconv.Atoi(width)
	if err != nil {
		http.Error(w, "Invalid width value", 400)
	}

	heightInt, err := strconv.Atoi(height)
	if err != nil {
		http.Error(w, "Invalid width value", 400)
	}

	// Setup websocket with the client
	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	defer conn.Close()

	// Connect to the container
	env := make(map[string]string)
	env["USER"] = "root"
	env["HOME"] = "/root"
	env["TERM"] = "xterm"

	inRead, inWrite := io.Pipe()
	outRead, outWrite := io.Pipe()

	// read handler
	go func(conn *websocket.Conn, r io.Reader) {
		in := shared.ReaderToChannel(r, -1)

		for {
			buf, ok := <-in
			if !ok {
				break
			}

			err = conn.WriteMessage(websocket.TextMessage, buf)
			if err != nil {
				break
			}
		}
	}(conn, outRead)

	// write handler
	go func(conn *websocket.Conn, w io.Writer) {
		for {
			mt, payload, err := conn.ReadMessage()
			if err != nil {
				if err != io.EOF {
					break
				}
			}

			switch mt {
			case websocket.BinaryMessage:
				continue
			case websocket.TextMessage:
				w.Write(payload)
			default:
				break
			}
		}
	}(conn, inWrite)

	// control socket handler
	handler := func(conn *websocket.Conn) {
		for {
			_, _, err = conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}

	req := api.ContainerExecPost{
		Command:     config.Command,
		WaitForWS:   true,
		Interactive: true,
		Environment: env,
		Width:       widthInt,
		Height:      heightInt,
	}

	execArgs := lxd.ContainerExecArgs{
		Stdin:    inRead,
		Stdout:   outWrite,
		Stderr:   outWrite,
		Control:  handler,
		DataDone: make(chan bool),
	}

	op, err := lxdDaemon.ExecContainer(containerName, req, &execArgs)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}

	err = op.Wait()
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}

	<-execArgs.DataDone

	inWrite.Close()
	outRead.Close()

}
