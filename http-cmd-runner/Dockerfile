# Build the static Linux binary, then ship it on a small base that has a shell
# and common coreutils (df, uptime, free, uname) for the default allowlist.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod .
COPY *.go .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/http-cmd-runner .

FROM alpine:3.20
RUN addgroup -S cmdrunner && adduser -S -G cmdrunner cmdrunner
COPY --from=build /out/http-cmd-runner /usr/local/bin/http-cmd-runner
COPY config.example.json /etc/http-cmd-runner/config.json
USER cmdrunner
EXPOSE 8080
ENTRYPOINT ["http-cmd-runner", "-config", "/etc/http-cmd-runner/config.json"]
