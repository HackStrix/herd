from golang:1.24-alpine as BUILDER

WORKDIR /herd

COPY . .

WORKDIR /herd/examples/playwright

RUN go mod download

RUN go build -o main main.go

from ubuntu:24.04 AS RUNNER

RUN apt update && apt install -y nodejs npm


RUN npx playwright install chromium --with-deps

WORKDIR /app

COPY --from=BUILDER /herd/examples/playwright/main .

CMD ["./main"]
