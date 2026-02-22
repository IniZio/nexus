const express = require('express');
const router = express.Router();

// Basic health check
router.get('/', (req, res) => {
  res.json({
    status: 'healthy',
    timestamp: new Date().toISOString(),
    uptime: process.uptime(),
    memory: process.memoryUsage()
  });
});

// Detailed health with dependency checks
router.get('/detailed', async (req, res) => {
  const checks = {
    api: { status: 'healthy', responseTime: 0 },
    database: { status: 'unknown', responseTime: 0 },
    cache: { status: 'unknown', responseTime: 0 }
  };

  // Check database
  const dbStart = Date.now();
  try {
    const { Pool } = require('pg');
    const pool = new Pool({
      connectionString: process.env.DATABASE_URL
    });
    await pool.query('SELECT 1');
    await pool.end();
    checks.database = { status: 'healthy', responseTime: Date.now() - dbStart };
  } catch (err) {
    checks.database = { status: 'unhealthy', error: err.message };
  }

  // Check Redis
  const redisStart = Date.now();
  try {
    const redis = require('redis');
    const client = redis.createClient({
      url: process.env.REDIS_URL
    });
    await client.connect();
    await client.ping();
    await client.disconnect();
    checks.cache = { status: 'healthy', responseTime: Date.now() - redisStart };
  } catch (err) {
    checks.cache = { status: 'unhealthy', error: err.message };
  }

  const allHealthy = Object.values(checks).every(c => c.status === 'healthy');

  res.status(allHealthy ? 200 : 503).json({
    status: allHealthy ? 'healthy' : 'degraded',
    timestamp: new Date().toISOString(),
    checks
  });
});

module.exports = router;
