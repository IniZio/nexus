FROM python:3.11-slim

# Install system dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libpq-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

EXPOSE 8000

CMD ["python", "manage.py", "runserver", "0.0.0.0:8000"]
