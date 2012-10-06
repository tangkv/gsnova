package proxy

import (
	"bufio"
	"bytes"
	"common"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"text/template"
	"util"
)

type JsonRule struct {
	Method       []string
	Host         []string
	URL          []string
	Proxy        []string
	Attr         map[string]string
	method_regex []*regexp.Regexp
	host_regex   []*regexp.Regexp
	url_regex    []*regexp.Regexp
}

func matchRegexs(str string, rules []*regexp.Regexp) bool {
	if len(rules) == 0 {
		return true
	}
	for _, regex := range rules {
		if regex.MatchString(str) {
			return true
		}
	}
	return false
}

func initRegexSlice(rules []string) ([]*regexp.Regexp, error) {
	regexs := make([]*regexp.Regexp, 0)
	for _, originrule := range rules {
		rule := strings.TrimSpace(originrule)
		rule = strings.Replace(rule, ".", "\\.", -1)
		rule = strings.Replace(rule, "*", ".*", -1)

		reg, err := regexp.Compile(rule)
		if nil != err {
			log.Printf("Invalid pattern:%s for reason:%v\n", originrule, err)
			return nil, err
		} else {
			regexs = append(regexs, reg)
		}
	}

	return regexs, nil
}

func (r *JsonRule) init() (err error) {
	r.method_regex, err = initRegexSlice(r.Method)
	if nil != err {
		return
	}
	r.host_regex, err = initRegexSlice(r.Host)
	if nil != err {
		return
	}
	r.url_regex, err = initRegexSlice(r.URL)

	return
}

func (r *JsonRule) match(req *http.Request) bool {
	return matchRegexs(req.Method, r.method_regex) && matchRegexs(req.Host, r.host_regex) && matchRegexs(req.RequestURI, r.url_regex)
}

type SpacConfig struct {
	defaultRule string
	rules       []*JsonRule
}

var spac *SpacConfig

var registedRemoteConnManager map[string]RemoteConnectionManager = make(map[string]RemoteConnectionManager)

func RegisteRemoteConnManager(connManager RemoteConnectionManager) {
	registedRemoteConnManager[connManager.GetName()] = connManager
}

var pacGenFormatter = `/*
 * Proxy Auto-Config file generated by autoproxy2pac
 *  Rule source: {{.RuleListUrl}}
 *  Last update: {{.RuleListDate}}
 */
function FindProxyForURL(url, host) {
	var {{.ProxyVar}} = "{{.ProxyString}}";
	var {{.DefaultVar}} = "{{.DefaultString}}";
	{{.CustomCodePre}}
	{{.RulesBegin}}
	{{.RuleListCode}}
	{{.RulesEnd}}
	{{.CustomCodePost}}
	return {{.DefaultVar}};
}`

func generatePAC(url, date, content string) string {
	// Prepare some data to insert into the template.
	type PACContent struct {
		RuleListUrl, RuleListDate     string
		ProxyVar, ProxyString         string
		DefaultVar, DefaultString     string
		CustomCodePre, CustomCodePost string
		RulesBegin, RulesEnd          string
		RuleListCode                  string
	}
	var pac = &PACContent{}
	pac.RulesBegin = "//-- AUTO-GENERATED RULES, DO NOT MODIFY!"
	pac.RulesEnd = "//-- END OF AUTO-GENERATED RULES"
	pac.ProxyVar = "PROXY"
	pac.RuleListUrl = url
	pac.RuleListDate = date
	pac.ProxyString = "PROXY " + net.JoinHostPort("127.0.0.1", common.ProxyPort)
	pac.DefaultVar = "DEFAULT"
	pac.DefaultString = "DIRECT"
	jscode := []string{}

	if usercontent, err := ioutil.ReadFile(common.Home + "/user-gfwlist.txt"); nil == err {
		content = content + "\n" + string(usercontent)
	}
	reader := bufio.NewReader(strings.NewReader(content))
	i := 0
	for {
		line, _, err := reader.ReadLine()
		if nil != err {
			break
		}
		//from the second line
		i = i + 1
		if i == 1 {
			continue
		}
		str := string(line)
		str = strings.TrimSpace(str)

		proxyVar := "PROXY"
		//comment
		if strings.HasPrefix(str, "!") || len(str) == 0 {
			continue
		}
		if strings.HasPrefix(str, "@@") {
			str = str[2:]
			proxyVar = "DEFAULT"
		}
		jsRegexp := ""

		if strings.HasPrefix(str, "/") && strings.HasSuffix(str, "/") {
			jsRegexp = str[1 : len(str)-1]
		} else {
			//			debug := false
			//			if strings.Contains(str, "17e.org") {
			//				debug = true
			//			}

			if tmp, err := regexp.Compile("\\*+"); err == nil {
				jsRegexp = tmp.ReplaceAllString(str, "*")
			}

			if tmp, err := regexp.Compile("\\^\\|$"); err == nil {
				jsRegexp = util.RegexpReplace(jsRegexp, "^", tmp, 1)
			}
			if tmp, err := regexp.Compile("(\\W)"); err == nil {
				jsRegexp = tmp.ReplaceAllString(jsRegexp, "\\$0")
			}
			jsRegexp = strings.Replace(jsRegexp, "\\*", ".*", -1)

			if tmp, err := regexp.Compile("\\\\^"); err == nil {
				jsRegexp = tmp.ReplaceAllString(jsRegexp, "(?:[^\\w\\-.%\u0080-\uFFFF]|$)")
			}

			if tmp, err := regexp.Compile("^\\\\\\|\\\\\\|"); err == nil {
				jsRegexp = util.RegexpReplace(jsRegexp, "^[\\w\\-]+:\\/+(?!\\/)(?:[^\\/]+\\.)?", tmp, 1)
			}
			//			if debug {
			//				log.Printf("1-- %s\n", jsRegexp)
			//			}
			if tmp, err := regexp.Compile("^\\\\\\|"); err == nil {
				jsRegexp = util.RegexpReplace(jsRegexp, "^", tmp, 1)
			}
			if tmp, err := regexp.Compile("\\\\\\|$"); err == nil {
				jsRegexp = util.RegexpReplace(jsRegexp, "$", tmp, 1)
			}
			if tmp, err := regexp.Compile("^(\\.\\*)"); err == nil {
				jsRegexp = util.RegexpReplace(jsRegexp, "", tmp, 1)
			}
			if tmp, err := regexp.Compile("(\\.\\*)$"); err == nil {
				jsRegexp = util.RegexpReplace(jsRegexp, "", tmp, 1)
			}
			if len(jsRegexp) == 0 {
				log.Printf("There is one rule that matches all URL, which is highly *NOT* recommended: %s\n", str)
			}
		}
		jsLine := fmt.Sprintf("if(/%s/i.test(url)) return %s;", jsRegexp, proxyVar)
		if proxyVar == "DEFAULT" {
			//log.Printf("%s\n", jsLine)
			jscode = append(jscode[:0], append([]string{jsLine}, jscode[0:]...)...)
		} else {
			jscode = append(jscode, jsLine)
		}
	}
	pac.RuleListCode = strings.Join(jscode, "\r\n\t")
	t := template.Must(template.New("pacGenFormatter").Parse(pacGenFormatter))
	var buffer bytes.Buffer
	err := t.Execute(&buffer, pac)
	if err != nil {
		log.Println("Executing template:", err)
	}
	return buffer.String()
}

func generatePACFromGFWList(url string) {
	log.Printf("Generate PAC from  gfwlist %s\n", url)
	resp, err := util.HttpGet(url, "")
	if err != nil {
		if addr, exist := common.Cfg.GetProperty("LocalServer", "Listen"); exist {
			_, port, _ := net.SplitHostPort(addr)
			resp, err = util.HttpGet(url, "http://"+net.JoinHostPort("127.0.0.1", port))
		}
		resp, err = http.DefaultClient.Get(url)
	}
	if err != nil || resp.StatusCode != 200 {
		log.Printf("Failed to fetch AutoProxy2PAC from %s for reason:%v  %v\n", url, err)
	} else {
		body, err := ioutil.ReadAll(resp.Body)
		if nil == err {
			last_mod_date := resp.Header.Get("last-modified")
			hf := common.Home + "/snova-gfwlist.pac"
			content, _ := base64.StdEncoding.DecodeString(string(body))
			file_content := generatePAC(url, last_mod_date, string(content))
			ioutil.WriteFile(hf, []byte(file_content), 0666)
		}
	}
}

func PostInitSpac() {
	switch spac.defaultRule {
	case GAE_NAME:
		if !gae_enable {
			spac.defaultRule = DIRECT_NAME
			injectCRLFPatterns = initHostMatchRegex("*")
		}
	case C4_NAME:
		if !c4_enable {
			spac.defaultRule = DIRECT_NAME
			injectCRLFPatterns = initHostMatchRegex("*")
		}
	}
}

func InitSpac() {
	spac = &SpacConfig{}
	spac.defaultRule, _ = common.Cfg.GetProperty("SPAC", "Default")
	if len(spac.defaultRule) == 0 {
		spac.defaultRule = GAE_NAME
	}
	spac.rules = make([]*JsonRule, 0)
	if enable, exist := common.Cfg.GetIntProperty("SPAC", "Enable"); exist {
		if enable == 0 {
			return
		}
	}
	if url, exist := common.Cfg.GetProperty("SPAC", "GFWList"); exist {
		go generatePACFromGFWList(url)
	}

	script, exist := common.Cfg.GetProperty("SPAC", "Script")
	if !exist {
		script = "spac.json"
	}
	file, e := ioutil.ReadFile(common.Home + script)
	if e == nil {
		e = json.Unmarshal(file, &spac.rules)
		for _, json_rule := range spac.rules {
			e = json_rule.init()
		}
	}
	if nil != e {
		log.Printf("Failed to init SPAC for reason:%v", e)
	}
}

func SelectProxy(req *http.Request, conn net.Conn, isHttpsConn bool) []RemoteConnectionManager {
	host := req.Host
	port := "80"
	if v, p, err := net.SplitHostPort(req.Host); nil == err {
		host = v
		port = p
	}
	proxyNames := []string{spac.defaultRule}
	proxyManagers := make([]RemoteConnectionManager, 0)
	if (host == "127.0.0.1" || host == util.GetLocalIP()) && port == common.ProxyPort {
		handleSelfHttpRequest(req, conn)
		return nil
	}

	if !isHttpsConn && needRedirectHttpsHost(req.Host) {
		redirectHttps(conn, req)
		return nil
	}

	matched := false
	for _, r := range spac.rules {
		if r.match(req) {
			proxyNames = r.Proxy
			matched = true
			break
		}
	}

	if !isHttpsConn && !matched && needInjectCRLF(req.Host) {
		proxyNames = []string{"Direct", spac.defaultRule}
		matched = true
	}

	if !matched && hostsEnable != HOSTS_DISABLE {
		if _, exist := lookupReachableMappingHost(req, net.JoinHostPort(host, port)); exist {
			proxyNames = []string{"Direct", spac.defaultRule}
		} else {
			//log.Printf("[WARN]No available IP for %s\n", host)
		}
	}
	//log.Printf("Matchd %s with host %s\n", proxyNames[0], req.Host)
	for _, proxyName := range proxyNames {
		if strings.EqualFold(proxyName, DEFAULT_NAME) {
			proxyName = spac.defaultRule
		}
		switch proxyName {
		case GAE_NAME, C4_NAME:
			if v, ok := registedRemoteConnManager[proxyName]; ok {
				proxyManagers = append(proxyManagers, v)
			} else {
				log.Printf("No proxy:%s defined\n", proxyName)
			}
		case GOOGLE_NAME, GOOGLE_HTTP_NAME:
			if google_enable {
				proxyManagers = append(proxyManagers, httpGoogleManager)
			}
		case GOOGLE_HTTPS_NAME:
			if google_enable {
				proxyManagers = append(proxyManagers, httpsGoogleManager)
			}
		case DIRECT_NAME:
			forward := &Forward{overProxy: false}
			forward.target = req.Host
			if !strings.Contains(forward.target, ":") {
				forward.target = forward.target + ":80"
			}
			if !strings.Contains(forward.target, "://") {
				forward.target = "http://" + forward.target
			}
			proxyManagers = append(proxyManagers, forward)
		default:
			forward := &Forward{overProxy: true}
			forward.target = strings.TrimSpace(proxyName)
			if !strings.Contains(forward.target, "://") {
				forward.target = "http://" + forward.target
			}
			proxyManagers = append(proxyManagers, forward)
		}
	}

	return proxyManagers
}
