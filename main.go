package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type RuleReq struct {
	GwIP string   `json:"gwip"`
	Ip   []string `json:"ip"`
}

type RegisterReq struct {
	GwIP      string `json:"gwip"`
	CoreMac   string `json:"coremac"`
	AccessMac string `json:"accessmac"`
	Hostname  string `json:"hostname"`
}

type GWRegisterReq struct {
	GwIP  string `json:"gwip"`
	GwMac string `json:"gwmac"`
}

type operation int

const (
	ruleAdd operation = iota
	ruleDel
	arpAdd
	arpDel
)

var addedRule map[string]string
var registeredUPFs map[string]string // [gwip] coremac

func main() {
	log.SetLevel(log.TraceLevel)
	log.Traceln("application started")
	addedRule = make(map[string]string)
	registeredUPFs = make(map[string]string)
	http.HandleFunc("/addrule", addRuleHandler)
	http.HandleFunc("/register", registerHandler)
	server := http.Server{Addr: ":8080"}

	server.ListenAndServe()

}

func addRuleHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "PUT":
		fallthrough
	case "POST":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Errorln("http req read body failed.")
			sendHTTPResp(http.StatusBadRequest, w)
		}

		log.Traceln(string(body))

		//var nwSlice NetworkSlice
		var rulereq RuleReq
		//fmt.Println("parham log : http body = ", body)
		err = json.Unmarshal(body, &rulereq)
		if err != nil || rulereq.GwIP == "" || len(rulereq.Ip) == 0 {
			log.Errorln("Json unmarshal failed for http request")
			sendHTTPResp(http.StatusBadRequest, w)
		}
		for _, i := range rulereq.Ip {
			added := false
			gwip, added := addedRule[i]
			if !added {
				err = execRule(rulereq.GwIP, i, ruleAdd)
				if err != nil {
					sendHTTPResp(http.StatusInternalServerError, w)
					return
				}
				err = execArp(rulereq.GwIP, i, arpAdd)
				if err != nil {
					sendHTTPResp(http.StatusInternalServerError, w)
					return
				}
				addedRule[i] = rulereq.GwIP
				continue
			}
			if rulereq.GwIP != gwip {
				err = execRule(rulereq.GwIP, i, ruleAdd)
				if err != nil {
					sendHTTPResp(http.StatusInternalServerError, w)
					return
				}
				err = execArp(rulereq.GwIP, i, arpAdd)
				if err != nil {
					sendHTTPResp(http.StatusInternalServerError, w)
					return
				}
				err = execRule(gwip, i, ruleDel)
				if err != nil {
					sendHTTPResp(http.StatusInternalServerError, w)
					return
				}
				err = execArp(gwip, i, arpDel)
				if err != nil {
					sendHTTPResp(http.StatusInternalServerError, w)
					return
				}

				addedRule[i] = rulereq.GwIP
			}
		}
		if err != nil {
			sendHTTPResp(http.StatusInternalServerError, w)
			return
		}
		sendHTTPResp(http.StatusCreated, w)
	default:
		log.Traceln(w, "Sorry, only PUT and POST methods are supported.")
		sendHTTPResp(http.StatusMethodNotAllowed, w)
	}
}

func markFromIP(ip string) string {
	octets := strings.Split(ip, ".")
	if len(octets) == 4 {
		mark := octets[3]
		return mark
	} else {
		log.Errorln("invalind gateway ip format. GwIP = ", ip)
		return ""
	}
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "PUT":
		fallthrough
	case "POST":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Errorln("http req read body failed.")
			sendHTTPResp(http.StatusBadRequest, w)
		}

		log.Traceln(string(body))

		//var nwSlice NetworkSlice
		var regReq RegisterReq
		//fmt.Println("parham log : http body = ", body)
		err = json.Unmarshal(body, &regReq)
		if err != nil || regReq.CoreMac == "" || regReq.GwIP == "" {
			log.Errorln("Json unmarshal failed for http request")
			sendHTTPResp(http.StatusBadRequest, w)
		}
		if regUPFCore, ok := registeredUPFs[regReq.GwIP]; ok && regUPFCore == regReq.CoreMac {
			sendHTTPResp(http.StatusCreated, w)
			return
		}
		iface := getifaceName(regReq.GwIP)
		go sendGWMac(iface, regReq.Hostname, regReq.GwIP)

		registeredUPFs[regReq.GwIP] = regReq.CoreMac
		sendHTTPResp(http.StatusCreated, w)
		return

	default:
		log.Traceln(w, "Sorry, only PUT and POST methods are supported.")
		sendHTTPResp(http.StatusMethodNotAllowed, w)
	}
}

func GetMac(ifname string) string {

	// Get the list of network interfaces.
	ifaces, err := net.Interfaces()
	if err != nil {
		fmt.Println("Error:", err)
		return ""
	}

	// Find the interface with the specified name.
	var targetInterface net.Interface
	for _, iface := range ifaces {
		if iface.Name == ifname {
			targetInterface = iface
			break
		}
	}

	if targetInterface.Name == "" {
		return ""
	}

	return targetInterface.HardwareAddr.String()
}

func sendGWMac(ifname, hostname, gwIP string) {
	gwMac := GetMac(ifname)
	GWRegisterReq := GWRegisterReq{
		GwIP:  gwIP, //access gw
		GwMac: gwMac,
	}

	registerReqJson, _ := json.Marshal(GWRegisterReq)

	requestURL := fmt.Sprintf("http://%v-http:8080/registergw", hostname)

	jsonBody := []byte(registerReqJson)

	bodyReader := bytes.NewReader(jsonBody)
	req, err := http.NewRequest(http.MethodPost, requestURL, bodyReader)
	if err != nil {
		log.Errorf("client: could not create request: %s\n", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := http.Client{
		Timeout: 10 * time.Second,
	}
	done := false
	for !done {
		resp, err := client.Do(req)
		if err != nil {
			log.Errorf("client: error making http request: %s\n", err)
		} else if resp.StatusCode == http.StatusCreated {
			done = true
			log.Traceln("access mac Successfuly registered in host : ", hostname)
			return
		}

		time.Sleep(1 * time.Second)
	}

}

func getifaceName(gwIp string) string {
	cmd := exec.Command("sh", "-c", "ifconfig | grep -B1 "+gwIp+" | head -n1 | awk '{print $1;}'")

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Error running ip command: %v\n", err)
		return ""
	}
	lines := strings.Split(string(output), "\n")
	// Parse the route information to extract the gateway IP address

	iface := lines[0]

	return iface

}

func execRule(gwip, ueip string, op operation) error {
	mark := markFromIP(gwip)
	var oper string
	switch op {
	case ruleAdd:
		oper = "-A"
	case ruleDel:
		oper = "-D"
	}
	cmd := exec.Command("iptables", "-t", "mangle", oper, "PREROUTING", "-d", ueip, "-j", "MARK", "--set-mark", mark)
	log.Traceln("executing command : ", cmd.String())
	combinedOutput, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Error executing command: %v\nCombined Output: %s", cmd.String(), combinedOutput)
		return err
	}
	log.Traceln("iptables rule applied successfully for ip : ", ueip)
	return nil
}

func execArp(gwip, ueip string, op operation) error {
	mark := markFromIP(gwip)
	iface := fmt.Sprint("upf", mark)
	var cmd *exec.Cmd

	switch op {
	case arpAdd:
		upfmac, ok := registeredUPFs[gwip]
		if !ok {
			errtxt := fmt.Sprint("upf connected to ", gwip, " is not registered in exitlb")
			return errors.New(errtxt)
		}
		cmd = exec.Command("arp", "-s", ueip, upfmac, "-i", iface)
	case arpDel:
		cmd = exec.Command("arp", "-d", ueip, "-i", iface)
	}
	log.Traceln("executing command : ", cmd.String())
	combinedOutput, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Error executing command: %v\nCombined Output: %s", cmd.String(), combinedOutput)
		return err
	}
	log.Traceln("static arp applied successfully for ip : ", ueip)
	return nil
}

func sendHTTPResp(status int, w http.ResponseWriter) {
	w.WriteHeader(status)
	w.Header().Set("Content-Type", "application/json")

	resp := make(map[string]string)

	switch status {
	case http.StatusCreated:
		resp["message"] = "Status Created"
	default:
		resp["message"] = "Failed to add slice"
	}

	jsonResp, err := json.Marshal(resp)
	if err != nil {
		log.Errorln("Error happened in JSON marshal. Err: ", err)
	}

	_, err = w.Write(jsonResp)
	if err != nil {
		log.Errorln("http response write failed : ", err)
	}
}
