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

func taskProxySignature(taskID int64) string {
	mac := hmac.New(sha256.New, taskProxySecret)
	_, _ = mac.Write([]byte(strconv.FormatInt(taskID, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// TaskProxyCredentials returns a process-local signed proxy identity. An agent
// can see its own credential but cannot change the task id and retain a valid
// signature, preventing a shell process from borrowing another task's policy.
func TaskProxyCredentials(taskID int64) (username, password string, ok bool) {
	if taskID <= 0 {
		return "", "", false
	}
	return taskProxyUserPrefix + strconv.FormatInt(taskID, 10), taskProxySignature(taskID), true
}

// ParseTaskProxyAuthorization extracts and verifies ARTEX's signed task tag.
// Ordinary proxy credentials are deliberately reported as untagged.
func ParseTaskProxyAuthorization(header string) (taskID int64, tagged bool, err error) {
	if !strings.HasPrefix(header, "Basic ") {
		return 0, false, nil
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if decodeErr != nil {
		return 0, false, nil
	}
	credentials := strings.SplitN(string(decoded), ":", 2)
	if len(credentials) != 2 || !strings.HasPrefix(credentials[0], taskProxyUserPrefix) {
		return 0, false, nil
	}
	taskID, parseErr := strconv.ParseInt(strings.TrimPrefix(credentials[0], taskProxyUserPrefix), 10, 64)
	if parseErr != nil || taskID <= 0 {
		return 0, true, errors.New("无效的任务代理标识")
	}
	expected := taskProxySignature(taskID)
	if len(credentials[1]) != len(expected) || subtle.ConstantTimeCompare([]byte(credentials[1]), []byte(expected)) != 1 {
		return 0, true, errors.New("任务代理签名无效")
	}
	return taskID, true, nil
}
