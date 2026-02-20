const request = require('supertest');
const app = require('../src/index');

describe('API Endpoints', () => {
  describe('Health Check', () => {
    it('GET /health should return healthy status', async () => {
      const res = await request(app).get('/health');
      expect(res.status).toBe(200);
      expect(res.body.status).toBe('healthy');
    });
  });

  describe('Users API', () => {
    it('GET /api/users should return user list', async () => {
      const res = await request(app).get('/api/users');
      expect(res.status).toBe(200);
      expect(Array.isArray(res.body)).toBe(true);
    });

    it('POST /api/users should create a new user', async () => {
      const newUser = {
        name: 'Test User',
        email: 'test@example.com',
        password: 'password123'
      };
      const res = await request(app)
        .post('/api/users')
        .send(newUser);
      expect(res.status).toBe(201);
      expect(res.body.name).toBe(newUser.name);
    });

    it('POST /api/users should reject invalid data', async () => {
      const invalidUser = {
        name: 'A',
        email: 'invalid',
        password: 'short'
      };
      const res = await request(app)
        .post('/api/users')
        .send(invalidUser);
      expect(res.status).toBe(400);
    });
  });

  describe('Products API', () => {
    it('GET /api/products should return product list', async () => {
      const res = await request(app).get('/api/products');
      expect(res.status).toBe(200);
      expect(Array.isArray(res.body)).toBe(true);
    });

    it('GET /api/products with filters should work', async () => {
      const res = await request(app)
        .get('/api/products')
        .query({ category: 'electronics', minPrice: 10 });
      expect(res.status).toBe(200);
    });
  });
});
