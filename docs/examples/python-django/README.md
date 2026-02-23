# Python + Django Workspace

**Time:** 20 minutes  
**Stack:** Python 3.11, Django 5, PostgreSQL, Redis

## Problem

Python development environments are notoriously tricky:
- System Python conflicts with project requirements
- Virtualenvs litter your filesystem
- Different projects need different Python versions
- Database setup is repetitive

## Setup

### Prerequisites

- Docker Desktop
- Nexus CLI
- Git repository

## Step 1: Create Workspace

```bash
nexus workspace create django-app --dind
```

## Step 2: Configure Environment

Create these files in `.worktrees/django-app/`:

**Dockerfile:**
```dockerfile
FROM python:3.11-slim

# Install system dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libpq-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

EXPOSE 8000

CMD ["python", "manage.py", "runserver", "0.0.0.0:8000"]
```

**requirements.txt:**
```
django>=5.0,<5.1
psycopg2-binary
redis
celery
django-debug-toolbar
pytest-django
black
flake8
```

**docker-compose.yml:**
```yaml
version: '3.8'
services:
  web:
    build: .
    command: python manage.py runserver 0.0.0.0:8000
    volumes:
      - .:/workspace
    ports:
      - "8000:8000"
    environment:
      - DEBUG=1
      - DATABASE_URL=postgres://django:django@db:5432/django
      - REDIS_URL=redis://redis:6379/0
    depends_on:
      - db
      - redis
  
  db:
    image: postgres:15-alpine
    environment:
      POSTGRES_DB: django
      POSTGRES_USER: django
      POSTGRES_PASSWORD: django
    volumes:
      - postgres_data:/var/lib/postgresql/data
    ports:
      - "5432:5432"
  
  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

## Step 3: Initialize Django Project

```bash
# Enter workspace
nexus workspace ssh django-app

# Create Django project
$ django-admin startproject myproject .

# Verify it works
$ python manage.py runserver 0.0.0.0:8000
```

## Step 4: Access from Host

```bash
# On host
nexus workspace port add django-app 8000
```

Open http://localhost:8000

## Result

✅ **What you achieved:**
- Isolated Python 3.11 environment
- Django development server with auto-reload
- PostgreSQL database in separate container
- Redis for caching/task queue
- No Python version conflicts on your host

## Development Workflow

```bash
# Start all services
$ docker-compose up -d

# Run migrations
$ python manage.py migrate

# Create superuser
$ python manage.py createsuperuser

# Run tests
$ pytest

# Check logs
$ docker-compose logs -f
```

## Cleanup

```bash
nexus workspace delete django-app
```
