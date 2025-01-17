package auth

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"time"

	goauth "github.com/abbot/go-http-auth"
	"github.com/pteich/traefik/log"
	"github.com/pteich/traefik/middlewares/accesslog"
	"github.com/pteich/traefik/middlewares/tracing"
	"github.com/pteich/traefik/types"
	"github.com/urfave/negroni"
)

// Authenticator is a middleware that provides HTTP basic and digest authentication
type Authenticator struct {
	handler negroni.Handler
	users   map[string]string
}

type tracingAuthenticator struct {
	name           string
	handler        negroni.Handler
	clientSpanKind bool
}

const (
	authorizationHeader = "Authorization"
)

// NewAuthenticator builds a new Authenticator given a config
func NewAuthenticator(authConfig *types.Auth, tracingMiddleware *tracing.Tracing) (*Authenticator, error) {
	if authConfig == nil {
		return nil, fmt.Errorf("error creating Authenticator: auth is nil")
	}

	var err error
	authenticator := &Authenticator{}
	tracingAuth := tracingAuthenticator{}

	if authConfig.Basic != nil {
		authenticator.users, err = parserBasicUsers(authConfig.Basic)
		if err != nil {
			return nil, err
		}

		basicAuth := goauth.NewBasicAuthenticator("traefik", authenticator.secretBasic)
		tracingAuth.handler = createAuthBasicHandler(basicAuth, authConfig)
		tracingAuth.name = "Auth Basic"
		tracingAuth.clientSpanKind = false
	} else if authConfig.Digest != nil {
		authenticator.users, err = parserDigestUsers(authConfig.Digest)
		if err != nil {
			return nil, err
		}

		digestAuth := goauth.NewDigestAuthenticator("traefik", authenticator.secretDigest)
		tracingAuth.handler = createAuthDigestHandler(digestAuth, authConfig)
		tracingAuth.name = "Auth Digest"
		tracingAuth.clientSpanKind = false
	} else if authConfig.Forward != nil {
		tracingAuth.handler, err = createAuthForwardHandler(authConfig)
		if err != nil {
			return nil, err
		}
		tracingAuth.name = "Auth Forward"
		tracingAuth.clientSpanKind = true
	}

	if tracingMiddleware != nil {
		authenticator.handler = tracingMiddleware.NewNegroniHandlerWrapper(tracingAuth.name, tracingAuth.handler, tracingAuth.clientSpanKind)
	} else {
		authenticator.handler = tracingAuth.handler
	}
	return authenticator, nil
}

func createAuthForwardHandler(authConfig *types.Auth) (negroni.HandlerFunc, error) {
	// Ensure our request client does not follow redirects
	client := http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	if authConfig.Forward.TLS != nil {
		tlsConfig, err := authConfig.Forward.TLS.CreateTLSConfig()
		if err != nil {
			return nil, err
		}

		client.Transport = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig:       tlsConfig,
		}
	}
	return negroni.HandlerFunc(func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		Forward(authConfig.Forward, client, w, r, next)
	}), nil
}
func createAuthDigestHandler(digestAuth *goauth.DigestAuth, authConfig *types.Auth) negroni.HandlerFunc {
	return negroni.HandlerFunc(func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		if username, _ := digestAuth.CheckAuth(r); username == "" {
			log.Debugf("Digest auth failed")
			digestAuth.RequireAuth(w, r)
		} else {
			log.Debugf("Digest auth succeeded")

			// set username in request context
			r = accesslog.WithUserName(r, username)

			if authConfig.HeaderField != "" {
				r.Header[authConfig.HeaderField] = []string{username}
			}
			if authConfig.Digest.RemoveHeader {
				log.Debugf("Remove the Authorization header from the Digest auth")
				r.Header.Del(authorizationHeader)
			}
			next.ServeHTTP(w, r)
		}
	})
}
func createAuthBasicHandler(basicAuth *goauth.BasicAuth, authConfig *types.Auth) negroni.HandlerFunc {
	return negroni.HandlerFunc(func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		if username := basicAuth.CheckAuth(r); username == "" {
			log.Debugf("Basic auth failed")
			basicAuth.RequireAuth(w, r)
		} else {
			log.Debugf("Basic auth succeeded")

			// set username in request context
			r = accesslog.WithUserName(r, username)

			if authConfig.HeaderField != "" {
				r.Header[authConfig.HeaderField] = []string{username}
			}
			if authConfig.Basic.RemoveHeader {
				log.Debugf("Remove the Authorization header from the Basic auth")
				r.Header.Del(authorizationHeader)
			}
			next.ServeHTTP(w, r)
		}
	})
}

func getLinesFromFile(filename string) ([]string, error) {
	dat, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	// Trim lines and filter out blanks
	rawLines := strings.Split(string(dat), "\n")
	var filteredLines []string
	for _, rawLine := range rawLines {
		line := strings.TrimSpace(rawLine)
		if line != "" && !strings.HasPrefix(line, "#") {
			filteredLines = append(filteredLines, line)
		}
	}
	return filteredLines, nil
}

func (a *Authenticator) secretBasic(user, realm string) string {
	if secret, ok := a.users[user]; ok {
		return secret
	}
	log.Debugf("User not found: %s", user)
	return ""
}

func (a *Authenticator) secretDigest(user, realm string) string {
	if secret, ok := a.users[user+":"+realm]; ok {
		return secret
	}
	log.Debugf("User not found: %s:%s", user, realm)
	return ""
}

func (a *Authenticator) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	a.handler.ServeHTTP(rw, r, next)
}
