FROM ubuntu:22.04

# Install basic tools
RUN apt-get update && apt-get install -y \
    curl \
    git \
    vim \
    nano \
    htop \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /workspace

# Default command
CMD ["/bin/bash"]
