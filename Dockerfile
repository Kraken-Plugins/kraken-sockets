FROM golang:1.26.1-alpine AS build

WORKDIR /app

# Add build dependencies for CGO (if needed) and certificates
RUN apk add --no-cache git ca-certificates tzdata

RUN adduser -D -g '' appuser

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify   # Ensures downloaded modules match go.sum checksums

COPY . .

# -ldflags: strips debug info (-w) and symbol table (-s) — reduces binary size significantly
# -trimpath: removes local file system paths from the binary
# CGO_ENABLED=0: fully static binary, no C dependencies
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -trimpath \
    -o main .


FROM scratch

# Carry over essentials from build stage
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /app/main /main

USER appuser

EXPOSE 8080
EXPOSE 26388

ENTRYPOINT ["/main"]