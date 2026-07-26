FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stack-backup ./cmd/stack-backup

FROM alpine:3.22
RUN apk add --no-cache restic ca-certificates tzdata
COPY --from=build /out/stack-backup /usr/local/bin/stack-backup
ENTRYPOINT ["stack-backup"]
