package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/dustinkirkland/golang-petname"
	"github.com/gorilla/websocket"
	"github.com/lxc/lxd"
	"github.com/lxc/lxd/shared"
	"github.com/satori/go.uuid"
)

func restStatusHandler(w http.ResponseWriter, r *http.Request) {
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

func restTermsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Generate the response
	body := make(map[string]interface{})
	body["hash"] = config.ServerTermsHash
	body["terms"] = config.ServerTerms

	err := json.NewEncoder(w).Encode(body)
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
}

func restStartHandler(w http.ResponseWriter, r *http.Request) {
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
	if requestTerms != config.ServerTermsHash {
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
	id := uuid.NewV4().String()

	var resp *lxd.Response
	if config.Container != "" {
		resp, err = lxdDaemon.LocalCopy(config.Container, containerName, nil, nil, false)
	} else {
		resp, err = lxdDaemon.Init(containerName, "local", config.Image, nil, nil, false)
	}

	if err != nil {
		restStartError(w, err, containerUnknownError)
		return
	}

	err = lxdDaemon.WaitForSuccess(resp.Operation)
	if err != nil {
		restStartError(w, err, containerUnknownError)
		return
	}

	// Configure the container
	ct, err := lxdDaemon.ContainerInfo(containerName)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	ct.Config["security.nesting"] = "true"
	if config.QuotaCPU > 0 {
		ct.Config["limits.cpu"] = fmt.Sprintf("%d", config.QuotaCPU)
	}

	if config.QuotaRAM > 0 {
		ct.Config["limits.memory"] = fmt.Sprintf("%dMB", config.QuotaRAM)
	}

	if config.QuotaProcesses > 0 {
		ct.Config["limits.processes"] = fmt.Sprintf("%d", config.QuotaProcesses)
	}

	if config.QuotaDisk > 0 {
		ct.Devices["root"] = shared.Device{"type": "disk", "path": "/", "size": fmt.Sprintf("%dGB", config.QuotaDisk)}
	}

	if !config.ServerConsoleOnly {
		ct.Config["user.user-data"] = fmt.Sprintf(`#cloud-config
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

	err = lxdDaemon.UpdateContainerConfig(containerName, ct.Brief())
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	// Start the container
	resp, err = lxdDaemon.Action(containerName, "start", -1, false, false)
	if err != nil {
		lxdForceDelete(lxdDaemon, containerName)
		restStartError(w, err, containerUnknownError)
		return
	}

	err = lxdDaemon.WaitForSuccess(resp.Operation)
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
			ct, err := lxdDaemon.ContainerState(containerName)
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Get the id
	id := r.FormValue("id")

	// Get the container
	containerName, containerIP, containerUsername, containerPassword, containerExpiry, err := dbGetContainer(id)
	if err != nil || containerName == "" {
		http.Error(w, "Container not found", 404)
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
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get the id argument
	id := r.FormValue("id")

	// Get the container
	containerName, _, _, _, _, err := dbGetContainer(id)
	if err != nil || containerName == "" {
		http.Error(w, "Container not found", 404)
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
		in := shared.ReaderToChannel(r)

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

	// writer handler
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
	handler := func(c *lxd.Client, conn *websocket.Conn) {
		for {
			w, err := conn.NextWriter(websocket.TextMessage)
			if err != nil {
				break
			}

			msg := shared.ContainerExecControl{}
			msg.Command = "window-resize"
			msg.Args = make(map[string]string)
			msg.Args["width"] = width
			msg.Args["height"] = height

			buf, err := json.Marshal(msg)
			if err != nil {
				break
			}
			_, err = w.Write(buf)

			w.Close()
			if err != nil {
				break
			}

			_, _, err = conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}

	_, err = lxdDaemon.Exec(containerName, []string{"bash"}, env, inRead, outWrite, outWrite, handler)

	inWrite.Close()
	outRead.Close()

	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
}
