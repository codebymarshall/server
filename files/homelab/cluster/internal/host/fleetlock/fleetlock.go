// Package fleetlock is bluefin-reboot-lock: take or give back this node's
// reboot slot on a FleetLock server, so a fleet without Kubernetes reboots a
// few nodes at a time.
//
// FleetLock is the HTTP protocol Zincati uses on Fedora CoreOS
// (https://coreos.github.io/zincati/development/fleetlock/protocol/). A POST
// to <URL>/v1/pre-reboot takes a slot in the node's group and a POST to
// <URL>/v1/steady-state gives it back. Both carry the header
// `fleet-lock-protocol: true` and the body
// {"client_params":{"id":"<id>","group":"<group>"}}; HTTP 200 means done, any
// other answer means not. Slots are owned and recursive.
//
//	acquire  before every local reboot: an ExecCondition= of
//	         systemd-sysupdate-reboot.service (20-interlock.conf), and
//	         bluefin-boot-deadline before it reboots a counted boot.
//	release  by bluefin-reboot-lock-release.service once a boot is good.
//
// Exit status:
//
//	0  the slot is held (acquire) or given back (release), or no server is
//	   configured;
//	1  refused: the server answered, but not with HTTP 200, or the
//	   configuration is bad (URL, group, client id);
//	2  usage error;
//	3  unreachable: no answer from the server at all (DNS, connect, TLS or
//	   timeout). The nightly reboot skips on any non-zero status, but
//	   bluefin-boot-deadline does not wait on 3: the update it rolls back may
//	   be what cut the node off.
//
// Configuration: URL= and GROUP= in /etc/bluefin/reboot-lock.conf; the
// system credentials bluefin.reboot-lock.url and bluefin.reboot-lock.group
// override the file. GROUP defaults to "default", as in Zincati. Without a
// URL both verbs do nothing and exit 0. The URL may carry credentials
// (https://user:password@host/); messages show it without them. Credentials
// on a plain http:// URL travel in clear; that is logged.
//
// The client id is the machine ID hashed with this tool's application key
// (systemd-id128 machine-id --app-specific=), so the machine ID itself never
// leaves the node.
package fleetlock

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/fetch"
	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const appID = "924987751bb64ad68c398e4be4e32e2f"

var (
	urlRE   = regexp.MustCompile(`^https?://[^ \t\n\v\f\r]+$`)
	groupRE = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
)

// Client is the HTTP client; tests replace its transport and sleep.
var Client = &fetch.Client{ConnectTimeout: 10 * time.Second, MaxTime: 30 * time.Second, Retries: 2}

func readConf(file string) (url, group string) {
	if !sys.Readable(file) {
		return "", ""
	}
	data, _ := os.ReadFile(file)
	for _, line := range sys.Lines(data, true) {
		key, value := sys.SplitEq(line)
		key = strings.Map(func(r rune) rune {
			if sys.IsSpace(r) {
				return -1
			}
			return r
		}, key)
		if key == "" {
			continue
		}
		value = sys.Unquote(sys.TrimSpace(strings.TrimSuffix(value, "\r")))
		switch key {
		case "URL":
			url = value
		case "GROUP":
			group = value
		}
	}
	return url, group
}

// logURL is the URL as messages show it: the authority only from its last
// "@" on, so https://user:password@host/ is logged as https://host/.
func logURL(url string) (shown string, authority string) {
	rest := url
	if _, after, ok := strings.Cut(url, "://"); ok {
		rest = after
	}
	authority, _, _ = strings.Cut(rest, "/")
	host := authority[strings.LastIndexByte(authority, '@')+1:]
	return strings.TrimSuffix(url, rest) + host + strings.TrimPrefix(rest, authority), authority
}

// Main is bluefin-reboot-lock. Test overrides: BLUEFIN_REBOOT_LOCK_CONF,
// CREDENTIALS_DIRECTORY (as systemd sets it); systemd-id128 through PATH.
func Main(h *sys.Host, args []string) int {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	var endpoint, fail string
	switch verb {
	case "acquire":
		endpoint, fail = "pre-reboot", "not rebooting"
	case "release":
		endpoint, fail = "steady-state", "not released"
	default:
		h.Eprintf("usage: bluefin-reboot-lock acquire|release\n")
		return 2
	}

	conf := h.Env("BLUEFIN_REBOOT_LOCK_CONF", "/etc/bluefin/reboot-lock.conf")
	url, group := readConf(conf)
	creds := h.Env("CREDENTIALS_DIRECTORY", "/nonexistent")
	for _, cred := range []struct {
		name string
		dst  *string
	}{{"url", &url}, {"group", &group}} {
		file := creds + "/bluefin.reboot-lock." + cred.name
		if sys.Readable(file) {
			value, _ := sys.ReadSubst(file)
			*cred.dst = strings.NewReplacer("\r", "", "\n", "").Replace(value)
		}
	}

	if url == "" {
		h.Log(sys.Info, "no FleetLock server configured ("+conf+", credential bluefin.reboot-lock.url); nothing to "+verb)
		return 0
	}
	if group == "" {
		group = "default"
	}
	shown, authority := logURL(url)
	if !urlRE.MatchString(url) {
		h.Log(sys.Err, "FleetLock URL "+shown+" is not an http(s) URL; "+fail)
		return 1
	}
	for strings.HasSuffix(url, "/") {
		url = strings.TrimSuffix(url, "/")
		shown = strings.TrimSuffix(shown, "/")
	}
	if strings.Contains(authority, "@") && strings.HasPrefix(url, "http://") {
		h.Log(sys.Warning, "FleetLock URL "+shown+" carries credentials over plain http; they are sent in clear")
	}
	if !groupRE.MatchString(group) {
		h.Log(sys.Err, "FleetLock group "+group+" must match [a-zA-Z0-9.-]+; "+fail)
		return 1
	}
	id, code := h.Output(false, "systemd-id128", "machine-id", "--app-specific="+appID)
	if code != 0 || id == "" {
		h.Log(sys.Err, "cannot derive the FleetLock client id from the machine ID; "+fail)
		return 1
	}

	body := `{"client_params":{"id":"` + id + `","group":"` + group + `"}}`
	who := "id " + id + ", group " + group
	client := *Client
	client.Sleep = h.Wait
	// No redirects: the protocol header guards against redirected requests.
	resp, err := client.Do(fetch.Request{
		Method: "POST",
		URL:    url + "/v1/" + endpoint,
		Header: map[string][]string{
			"fleet-lock-protocol": {"true"},
			"Content-Type":        {"application/json"},
		},
		Body: []byte(body),
	})
	if err != nil {
		// No HTTP answer at all, which is not a refusal (exit 3); what happens
		// next is the caller's call, so the message does not say.
		h.Eprintf("bluefin-reboot-lock: %v\n", err)
		h.Log(sys.Err, "FleetLock server "+shown+"/v1/"+endpoint+" is unreachable ("+who+"): curl exit "+curlCode(err))
		return 3
	}
	if resp.Code == 200 {
		if verb == "acquire" {
			h.Log(sys.Info, "holding a reboot slot on "+shown+" ("+who+")")
		} else {
			h.Log(sys.Info, "released the reboot slot on "+shown+" ("+who+")")
		}
		return 0
	}
	status := strconv.Itoa(resp.Code)
	if detail := sys.CutBytes(sys.StripControl(string(resp.Body)), 200); detail != "" {
		status += " " + detail
	}
	if verb == "acquire" {
		h.Log(sys.Warning, "no reboot slot on "+shown+" ("+who+"): HTTP "+status+"; "+fail)
	} else {
		h.Log(sys.Err, "the reboot slot on "+shown+" ("+who+") is "+fail+": HTTP "+status)
	}
	return 1
}

func curlCode(err error) string {
	var ferr *fetch.Error
	if errors.As(err, &ferr) {
		return strconv.Itoa(ferr.Code)
	}
	return "56"
}

// ReleaseCondition is bluefin-reboot-lock-release.service's ExecCondition=:
// only a good boot gives the slot back, blessed ("good") or not boot-counted
// ("clean"). A counted boot that has not been blessed keeps it, so the fleet
// does not reboot another node while this one is unhealthy.
// Test override: BLUEFIN_BLESS_BOOT.
func ReleaseCondition(h *sys.Host, _ []string) int {
	status, _ := h.Output(false, h.Env("BLUEFIN_BLESS_BOOT", "/usr/lib/systemd/systemd-bless-boot"), "status")
	switch status {
	case "good", "clean":
		return 0
	case "":
		status = "unknown"
	}
	h.Printf("boot is %s: keeping the reboot slot\n", status)
	return 1
}
