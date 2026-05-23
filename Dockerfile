FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./cpa-plusplus ./cmd/server/

FROM alpine:3.23

RUN apk add --no-cache tzdata

RUN mkdir /cpa-plusplus

COPY --from=builder ./app/cpa-plusplus /cpa-plusplus/cpa-plusplus

COPY config.example.yaml /cpa-plusplus/config.example.yaml

WORKDIR /cpa-plusplus

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./cpa-plusplus"]
