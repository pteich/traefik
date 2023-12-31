# WEBUI
FROM node:9 as webui

ENV WEBUI_DIR /src/webui
RUN mkdir -p $WEBUI_DIR

COPY ./webui/ $WEBUI_DIR/

WORKDIR $WEBUI_DIR

RUN yarn install
RUN npm run build

# BUILD
FROM golang:1.21-alpine as gobuild

RUN apk --update upgrade \
    && apk --no-cache --no-progress add git mercurial bash gcc musl-dev curl tar ca-certificates tzdata \
    && update-ca-certificates \
    && rm -rf /var/cache/apk/*

#RUN mkdir -p /usr/local/bin \
#    && curl -fsSL -o /usr/local/bin/go-bindata https://github.com/containous/go-bindata/releases/download/v1.0.0/go-bindata \
#    && chmod +x /usr/local/bin/go-bindata

WORKDIR /go/src/github.com/traefik/traefik

# Download go modules
COPY go.mod .
COPY go.sum .
RUN GO111MODULE=on GOPROXY=https://proxy.golang.org go mod download

COPY . /go/src/github.com/traefik/traefik

RUN rm -rf /go/src/github.com/traefik/traefik/static/
COPY --from=webui /src/static/ /go/src/github.com/traefik/traefik/static/

RUN ./script/make.sh generate binary

## IMAGE
FROM scratch

COPY --from=gobuild /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=gobuild /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=gobuild /go/src/github.com/traefik/traefik/dist/traefik /

EXPOSE 80
VOLUME ["/tmp"]

ENTRYPOINT ["/traefik"]
