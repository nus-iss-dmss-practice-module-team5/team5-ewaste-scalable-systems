FROM python:3.12.12-slim
WORKDIR /app
COPY src/matcher/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt PyMySQL==1.1.2 pytest==8.3.5 pytest-cov==6.0.0 bcrypt==4.3.0
COPY contracts/matching /app/contracts/matching
COPY src/matcher /app/src/matcher
ENV PYTHONPATH=/app/src/matcher PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
USER 65532:65532
CMD ["python", "-m", "unittest", "discover", "-s", "src/matcher/tests", "-v"]
