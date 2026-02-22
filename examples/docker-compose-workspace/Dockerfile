FROM node:20-alpine

WORKDIR /app

# Install dependencies
COPY api/package.json .
RUN npm install

# Copy application code
COPY api/ ./api/

EXPOSE 3000

CMD ["npm", "start"]
