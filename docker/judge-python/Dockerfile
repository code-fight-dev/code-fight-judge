FROM python:3.12-alpine

WORKDIR /workspace

RUN adduser -D -u 10001 runner

ENV PYTHONNOUSERSITE=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOME=/tmp

USER runner

CMD ["python3", "-B", "/workspace/main.py"]
