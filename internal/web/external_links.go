package web

import (
	"encoding/json"
	"html/template"
	"net"
	"net/url"
	"strings"

	"github.com/yuin/goldmark/ast"
)

const externalLinkPolicyKey = "external_links.policy"

var externalRelOrder = []string{"sponsored", "nofollow", "noopener", "noreferrer"}

// ExternalLinkPolicy controls how public pages render links that point outside the site.
type ExternalLinkPolicy struct {
	TargetBlank   bool                `json:"target_blank"`
	Rel           []string            `json:"rel"`
	Rules         []ExternalLinkRule  `json:"rules,omitempty"`
	internalHosts map[string]struct{} `json:"-"`
}

type ExternalLinkRule struct {
	Domain            string   `json:"domain"`
	IncludeSubdomains bool     `json:"include_subdomains"`
	TargetBlank       bool     `json:"target_blank"`
	Rel               []string `json:"rel"`
}

type ExternalLinkPolicyForm struct {
	DefaultTargetBlank bool
	DefaultRel         map[string]bool
	RelOptions         []ExternalLinkRelOption
	Rules              []ExternalLinkRuleForm
}

type ExternalLinkRelOption struct {
	Token string
	Label string
	Note  string
}

type ExternalLinkRuleForm struct {
	Index             int
	Domain            string
	IncludeSubdomains bool
	TargetBlank       bool
	Rel               map[string]bool
}

type externalLinkDecision struct {
	External    bool
	TargetBlank bool
	Rel         []string
}

func defaultExternalLinkPolicy() ExternalLinkPolicy {
	return ExternalLinkPolicy{
		TargetBlank: true,
		Rel:         []string{"noopener", "noreferrer"},
	}
}

func (s *Server) externalLinkPolicy() ExternalLinkPolicy {
	p := defaultExternalLinkPolicy()
	raw := strings.TrimSpace(s.store.Setting(externalLinkPolicyKey))
	if raw == "" {
		return p.WithInternalHosts(s.externalLinkInternalHostValues()...)
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		p = defaultExternalLinkPolicy()
	}
	p.Rel = cleanExternalRelTokens(p.Rel)
	for i := range p.Rules {
		p.Rules[i].Domain = cleanExternalDomain(p.Rules[i].Domain)
		p.Rules[i].Rel = cleanExternalRelTokens(p.Rules[i].Rel)
	}
	return p.WithInternalHosts(s.externalLinkInternalHostValues()...)
}

func (s *Server) externalLinkInternalHostValues() []string {
	st := s.site(s.defaultLang())
	if st.BaseURL == "" {
		return nil
	}
	return []string{st.BaseURL}
}

func (p ExternalLinkPolicy) WithInternalHosts(values ...string) ExternalLinkPolicy {
	hosts := map[string]struct{}{}
	for _, raw := range values {
		addExternalLinkHost(hosts, raw)
	}
	if len(hosts) > 0 {
		p.internalHosts = hosts
	}
	return p
}

func addExternalLinkHost(hosts map[string]struct{}, raw string) {
	host := externalLinkHostname(raw)
	if host == "" {
		return
	}
	hosts[host] = struct{}{}
	if strings.HasPrefix(host, "www.") {
		hosts[strings.TrimPrefix(host, "www.")] = struct{}{}
		return
	}
	hosts["www."+host] = struct{}{}
}

func externalLinkHostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "//") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	host = strings.TrimSuffix(host, ".")
	return host
}

func (p ExternalLinkPolicy) decision(raw string) externalLinkDecision {
	u, ok := parseHTTPExternalURL(raw)
	if !ok {
		return externalLinkDecision{}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || p.isInternalHost(host) {
		return externalLinkDecision{}
	}
	d := externalLinkDecision{External: true, TargetBlank: p.TargetBlank, Rel: cleanExternalRelTokens(p.Rel)}
	for _, rule := range p.Rules {
		if rule.matches(host) {
			d.TargetBlank = rule.TargetBlank
			d.Rel = cleanExternalRelTokens(rule.Rel)
			break
		}
	}
	return d
}

func parseHTTPExternalURL(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false
	}
	return u, true
}

func (p ExternalLinkPolicy) isInternalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	if _, ok := p.internalHosts[host]; ok {
		return true
	}
	if strings.HasPrefix(host, "www.") {
		_, ok := p.internalHosts[strings.TrimPrefix(host, "www.")]
		return ok
	}
	_, ok := p.internalHosts["www."+host]
	return ok
}

func (r ExternalLinkRule) matches(host string) bool {
	domain := cleanExternalDomain(r.Domain)
	if domain == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == domain {
		return true
	}
	return r.IncludeSubdomains && strings.HasSuffix(host, "."+domain)
}

func (p ExternalLinkPolicy) HTMLAttr(raw string) template.HTMLAttr {
	d := p.decision(raw)
	if !d.External {
		return ""
	}
	var attrs []string
	if d.TargetBlank {
		attrs = append(attrs, `target="_blank"`)
	}
	if len(d.Rel) > 0 {
		attrs = append(attrs, `rel="`+strings.Join(d.Rel, " ")+`"`)
	}
	if len(attrs) == 0 {
		return ""
	}
	return template.HTMLAttr(" " + strings.Join(attrs, " "))
}

func (v *View) ExternalLinkAttrs(raw string) template.HTMLAttr {
	if v == nil {
		return ""
	}
	p := v.ExternalLinks.WithInternalHosts(v.Site.BaseURL)
	return p.HTMLAttr(raw)
}

func decorateMarkdownLinks(doc ast.Node, policy *ExternalLinkPolicy) {
	if policy == nil {
		return
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		d := policy.decision(string(link.Destination))
		if !d.External {
			return ast.WalkContinue, nil
		}
		if d.TargetBlank {
			link.SetAttributeString("target", []byte("_blank"))
		}
		if len(d.Rel) > 0 {
			link.SetAttributeString("rel", []byte(strings.Join(d.Rel, " ")))
		}
		return ast.WalkContinue, nil
	})
}

func externalLinkPolicyFromForm(form url.Values) ExternalLinkPolicy {
	p := ExternalLinkPolicy{
		TargetBlank: form.Get("external_default_target_blank") == "1",
		Rel:         cleanExternalRelTokens(form["external_default_rel"]),
	}
	for _, index := range form["external_rule_index"] {
		index = strings.TrimSpace(index)
		if index == "" {
			continue
		}
		domain := cleanExternalDomain(form.Get("external_rule_domain_" + index))
		if domain == "" {
			continue
		}
		p.Rules = append(p.Rules, ExternalLinkRule{
			Domain:            domain,
			IncludeSubdomains: form.Get("external_rule_subdomains_"+index) == "1",
			TargetBlank:       form.Get("external_rule_target_blank_"+index) == "1",
			Rel:               cleanExternalRelTokens(form["external_rule_rel_"+index]),
		})
	}
	return p
}

func (p ExternalLinkPolicy) JSON() string {
	p.Rel = cleanExternalRelTokens(p.Rel)
	rules := p.Rules[:0]
	for _, rule := range p.Rules {
		rule.Domain = cleanExternalDomain(rule.Domain)
		rule.Rel = cleanExternalRelTokens(rule.Rel)
		if rule.Domain != "" {
			rules = append(rules, rule)
		}
	}
	p.Rules = rules
	b, _ := json.Marshal(p)
	return string(b)
}

func (p ExternalLinkPolicy) Form() ExternalLinkPolicyForm {
	form := ExternalLinkPolicyForm{
		DefaultTargetBlank: p.TargetBlank,
		DefaultRel:         relTokenMap(p.Rel),
		RelOptions:         externalLinkRelOptions(),
	}
	for i, rule := range p.Rules {
		if cleanExternalDomain(rule.Domain) == "" {
			continue
		}
		form.Rules = append(form.Rules, ExternalLinkRuleForm{
			Index:             i,
			Domain:            rule.Domain,
			IncludeSubdomains: rule.IncludeSubdomains,
			TargetBlank:       rule.TargetBlank,
			Rel:               relTokenMap(rule.Rel),
		})
	}
	return form
}

func externalLinkRelOptions() []ExternalLinkRelOption {
	return []ExternalLinkRelOption{
		{Token: "sponsored", Label: "sponsored", Note: "广告、赞助、商业合作链接；告诉搜索引擎这是付费或合作关系。"},
		{Token: "nofollow", Label: "nofollow", Note: "不主动给对方传递搜索权重；适合不想背书的外部链接。"},
		{Token: "noopener", Label: "noopener", Note: "新窗口打开时隔离原页面，防止对方页面反向控制本站窗口。"},
		{Token: "noreferrer", Label: "noreferrer", Note: "打开外站时不带来源地址；更隐私，但对方统计里看不到本站来源。"},
	}
}

func cleanExternalRelTokens(values []string) []string {
	seen := map[string]bool{}
	for _, v := range values {
		seen[strings.ToLower(strings.TrimSpace(v))] = true
	}
	out := make([]string, 0, len(externalRelOrder))
	for _, token := range externalRelOrder {
		if seen[token] {
			out = append(out, token)
		}
	}
	return out
}

func relTokenMap(values []string) map[string]bool {
	m := map[string]bool{}
	for _, token := range cleanExternalRelTokens(values) {
		m[token] = true
	}
	return m
}

func cleanExternalDomain(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	raw = strings.TrimPrefix(raw, "*.")
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		raw = externalLinkHostname(raw)
	} else {
		if i := strings.Index(raw, "/"); i >= 0 {
			raw = raw[:i]
		}
		if host, _, err := net.SplitHostPort(raw); err == nil {
			raw = host
		}
	}
	raw = strings.Trim(raw, " .")
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return ""
	}
	return raw
}
