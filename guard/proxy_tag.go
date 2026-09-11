package guard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const taskProxyUserPrefix = "artex-task-"

var taskProxySecret = func() []byte {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("generate task proxy credential secret: " + err.Error())
	}
	return secret
}()

func taskProxyIdentity(taskID int64, scopes ...string) string {
	identity := strconv.FormatInt(taskID, 10)
	if len(scopes) > 0 && scopes[0] != "" {
		identity += "~" + base64.RawURLEncoding.EncodeToString([]byte(scopes[0]))
	}
	return identity
}

func taskProxySignature(taskID int64, scopes ...string) string {
	mac := hmac.New(sha256.New, taskProxySecret)
	_, _ = mac.Write([]byte(taskProxyIdentity(taskID, scopes...)))
	return hex.EncodeToString(mac.Sum(nil))
}

// TaskProxyCredentials returns a process-local signed proxy identity. An agent
// can see its own credential but cannot change the task id and retain a valid
// signature, preventing a shell process from borrowing another task's policy.
func TaskProxyCredentials(taskID int64, scopes ...string) (username, password string, ok bool) {
	if taskID <= 0 {
		return "", "", false
	}
	return taskProxyUserPrefix + taskProxyIdentity(taskID, scopes...), taskProxySignature(taskID, scopes...), true
}

// ParseTaskProxyAuthorization extracts and verifies ARTEX's signed task tag.
// Ordinary proxy credentials are deliberately reported as untagged.
func ParseTaskProxyAuthorization(header string) (taskID int64, tagged bool, err error) {
	taskID, _, tagged, err = ParseTaskProxyScope(header)
	return
}

func ParseTaskProxyScope(header string) (taskID int64, scope string, tagged bool, err error) {
	if !strings.HasPrefix(header, "Basic ") {
		return 0, "", false, nil
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if decodeErr != nil {
		return 0, "", false, nil
	}
	credentials := strings.SplitN(string(decoded), ":", 2)
	if len(credentials) != 2 || !strings.HasPrefix(credentials[0], taskProxyUserPrefix) {
		return 0, "", false, nil
	}
	identity := strings.SplitN(strings.TrimPrefix(credentials[0], taskProxyUserPrefix), "~", 2)
	if len(identity) == 2 {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(identity[1])
		if decodeErr != nil || len(decoded) == 0 {
			return 0, "", true, errors.New("无效的任务代理归属")
		}
		scope = string(decoded)
	}
	taskID, parseErr := strconv.ParseInt(identity[0], 10, 64)
	if parseErr != nil || taskID <= 0 {
		return 0, "", true, errors.New("无效的任务代理标识")
	}
	expected := taskProxySignature(taskID, scope)
	if len(credentials[1]) != len(expected) || subtle.ConstantTimeCompare([]byte(credentials[1]), []byte(expected)) != 1 {
		return 0, "", true, errors.New("任务代理签名无效")
	}
	return taskID, scope, true, nil
}
