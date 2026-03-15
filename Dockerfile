FROM golang:1.25-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o livetranslator.exe ./cmd/livetranslator

FROM alpine:3.21
WORKDIR /app
COPY --from=builder /app/livetranslator.exe .
ENTRYPOINT ["cat"]
