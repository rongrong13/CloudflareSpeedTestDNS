FROM golang:1.24-alpine AS builder
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go mod tidy
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -ldflags "-s -w" -o cfstd .
FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=builder /app/cfstd ./cfstd
COPY --from=builder /app/ip.txt ./ip.txt
COPY --from=builder /app/ipv6.txt ./ipv6.txt
EXPOSE 8080
CMD ["./cfstd", "-web"]
