package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
)

// CSRFHeader は書き込みの要求が CSRF の値を載せるヘッダ名。
const CSRFHeader = "X-Flywheel-CSRF"

// csrf は server の起動ごとの CSRF の値と、それを cookie・meta で配る名前を持つ。
// 値はメモリ上だけに置く（server が落ちれば消える）。
type csrf struct {
	token      string
	cookieName string
}

func newCSRF(port int) (*csrf, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("csrf の値を作れない: %w", err)
	}
	return &csrf{
		token:      base64.RawURLEncoding.EncodeToString(b[:]),
		cookieName: "flywheel_csrf_" + strconv.Itoa(port),
	}, nil
}

// setCookie は HTML を返す応答で、今回の起動の値を cookie に設定する。
func (c *csrf) setCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.cookieName,
		Value:    c.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// valid は cookie とヘッダの両方があり、互いに一致し、今回の起動の値であるときだけ true。
func (c *csrf) valid(r *http.Request) bool {
	ck, err := r.Cookie(c.cookieName)
	if err != nil {
		return false
	}
	h := r.Header.Get(CSRFHeader)
	if h == "" || ck.Value == "" {
		return false
	}
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	return eq(ck.Value, h) && eq(h, c.token)
}

// csrfGuard はルーティングの前に、すべての POST の CSRF を検査する。拒否は
// core（next）を呼ばずに 403 forbidden_origin で返す。
func csrfGuard(c *csrf, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !c.valid(r) {
			writeError(w, http.StatusForbidden, CodeForbiddenOrigin, "CSRF の検査に失敗した（cookie とヘッダ "+CSRFHeader+" が揃い、今回の起動の値と一致する必要がある）")
			return
		}
		next.ServeHTTP(w, r)
	})
}
