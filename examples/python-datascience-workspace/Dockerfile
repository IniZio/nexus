FROM jupyter/scipy-notebook:latest

USER root

# Install additional system dependencies
RUN apt-get update && apt-get install -y \
    postgresql-client \
    libpq-dev \
    && rm -rf /var/lib/apt/lists/*

# Switch back to jovyan user
USER $NB_UID

# Install Python packages
COPY requirements.txt /tmp/
RUN pip install --no-cache-dir -r /tmp/requirements.txt

# Set working directory
WORKDIR /home/jovyan/work

# Create directories for notebooks and data
RUN mkdir -p /home/jovyan/work/notebooks /home/jovyan/work/data

EXPOSE 8888

# Default command is inherited from base image
