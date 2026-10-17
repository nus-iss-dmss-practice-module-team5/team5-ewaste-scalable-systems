FROM python:3.12-slim AS runtime
LABEL ewaste.component="matcher"
WORKDIR /app
RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*
COPY src/matcher/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir -r /tmp/requirements.txt
COPY contracts/matching /app/contracts/matching
COPY src/matcher /app/src/matcher
ENV PYTHONPATH=/app/src/matcher PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
USER 65532:65532

FROM runtime AS matching-worker
LABEL ewaste.worker="matching"
CMD ["python", "-m", "matcher", "run-matching"]

FROM runtime AS analytics-worker
LABEL ewaste.worker="analytics"
CMD ["python", "-m", "matcher", "run-analytics"]

# Existing deployment scripts build without a target; retain their combined mode.
FROM runtime AS legacy
CMD ["python", "-m", "matcher", "run"]
