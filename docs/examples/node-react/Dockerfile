FROM node:20-slim

# Install git for development
RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# Note: package.json will be mounted from host via sync
# Dependencies installed at runtime

EXPOSE 5173

CMD ["npm", "run", "dev"]
