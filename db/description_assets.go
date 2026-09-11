package db

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var descriptionURLs = regexp.MustCompile(`(?i)https?://[^\s，；。‘’“”"'<>\x60()]+`)

// RegisterDescriptionAsset accepts only a literal, source-backed operator target.
// It is wired exclusively into the goals decomposer, not the general tool registry.
func (s *AssetStore) RegisterDescriptionAsset(taskID int64, kind, value, evidence string) (int64, error) {
	var description, goal string
	if err := s.db.QueryRow(`SELECT description,goal FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&description, &goal); err != nil {
		return 0, err
	}
	evidence = strings.TrimSpace(evidence)
	value = strings.TrimSpace(value)
	if evidence == "" || (!strings.Contains(description, evidence) && !strings.Contains(goal, evidence)) {
		return 0, fmt.Errorf("依据必须逐字引用当前任务描述或目标")
	}
	// Inspect the containing source sentence too: quoting just the host from
	// "禁止测试 host" must not strip the user's denial from the evidence.
	contextText := evidence
	for _, original := range []string{description, goal} {
		for _, sentence := range strings.FieldsFunc(original, func(r rune) bool { return strings.ContainsRune("。；;\n", r) }) {
			if strings.Contains(sentence, evidence) {
				contextText += "\n" + sentence
			}
		}
	}
	for _, deny := range []string{"禁止", "不测试", "不测", "不碰", "排除", "例如", "举例", "参考", "do not ", "don't ", "for example", "example:", "example：", "exclude "} {
		if strings.Contains(strings.ToLower(contextText), deny) {
			return 0, fmt.Errorf("依据包含排除或参考语句，不能登记为用户授权")
		}
	}
	// Compare whole literals, not substring matches such as evil-example.com.
	grantKind := "host"
	target := DomainKey(value)
	if strings.HasPrefix(target, "*.") {
		grantKind = "domain"
		target = strings.TrimPrefix(target, "*.")
	}
	if kind == "cidr" {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return 0, err
		}
		target = network.String()
		grantKind = "cidr"
	} else {
		var err error
		target, err = NormalizeAgentHost(target, true)
		if err != nil {
			return 0, err
		}
	}
	matched := false
	literals := strings.FieldsFunc(evidence, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，；。‘’“”\"'`()<>，：", r)
	})
	literals = append(literals, descriptionURLs.FindAllString(evidence, -1)...)
	for _, token := range literals {
		token = strings.Trim(token, ",;.!?")
		if strings.Contains(token, "://") {
			if u, e := url.Parse(token); e == nil {
				token = u.Hostname()
			}
		}
		if grantKind == "domain" && !strings.HasPrefix(token, "*.") {
			continue
		}
		token = strings.TrimPrefix(token, "*.")
		if grantKind == "cidr" {
			if _, n, e := net.ParseCIDR(token); e == nil && n.String() == target {
				matched = true
			}
		} else if h, e := NormalizeAgentHost(stripHostPort(token), true); e == nil && h == target {
			matched = true
		}
	}
	if !matched {
		return 0, fmt.Errorf("资产与原文目标不一致，不能扩大授权范围")
	}
	return s.withCompanyScopeMutation(func(scoped *AssetStore) (int64, error) {
		if _, err := scoped.tx.Exec(`SELECT set_config('artex.user_asset_registration','on',true)`); err != nil {
			return 0, err
		}
		root := ""
		if grantKind != "cidr" && net.ParseIP(target) == nil {
			root, _ = RootDomain(target)
		}
		if _, err := scoped.tx.Exec(`INSERT INTO task_asset_grants(task_id,kind,value,root_domain,source,evidence) VALUES($1,$2,$3,$4,'description',$5) ON CONFLICT DO NOTHING`, taskID, grantKind, target, root, evidence); err != nil {
			return 0, err
		}
		var id int64
		var err error
		if grantKind == "cidr" {
			err = scoped.upsertTaskScope(TaskScope{TaskID: taskID, Kind: "cidr", Net: target, Source: "manual", Reason: evidence})
			if err != nil {
				return 0, err
			}
			return 0, reconcileAssetTemplate(scoped.tx, taskID)
		}
		if net.ParseIP(target) != nil {
			id, err = scoped.UpsertIP(UpsertIPReq{IP: target, TaskID: taskID})
		} else {
			_, isApex := RootDomain(target)
			if grantKind == "domain" || isApex {
				id, err = scoped.UpsertRootDomain(UpsertRootDomainReq{Domain: target, TaskID: taskID})
			} else {
				id, err = scoped.UpsertSubdomain(UpsertSubdomainReq{Domain: target, TaskID: taskID})
			}
		}
		if err != nil {
			return 0, err
		}
		if err = authorizeUserAsset(scoped, taskID, id, "task", "任务描述："+evidence, false); err != nil {
			return 0, err
		}
		return id, reconcileAssetTemplate(scoped.tx, taskID)
	})
}
