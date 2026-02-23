FROM node:20-slim

# Install git
RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

EXPOSE 3000 5173

CMD ["npm", "run", "dev"]
