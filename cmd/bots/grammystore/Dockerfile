FROM node:22-alpine
WORKDIR /opt/grammystore
COPY package*.json ./
RUN npm ci --omit=dev
COPY src/ ./src/
CMD ["npm", "start"]
