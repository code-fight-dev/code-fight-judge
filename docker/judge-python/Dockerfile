FROM python:3.12-alpine

WORKDIR /workspace

RUN adduser -D -u 10001 runner

USER runner

CMD ["python3", "-B", "/workspace/main.py"]
