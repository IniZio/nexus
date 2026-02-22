const express = require('express');
const router = express.Router();
const { Pool } = require('pg');
const redis = require('redis');

// Database pool
const pool = new Pool({
  connectionString: process.env.DATABASE_URL,
  connectionTimeoutMillis: 5000
});

// Get all users
router.get('/', async (req, res) => {
  try {
    const result = await pool.query('SELECT * FROM users ORDER BY created_at DESC');
    res.json({
      users: result.rows,
      count: result.rowCount
    });
  } catch (err) {
    console.error('Database error:', err);
    res.status(500).json({ error: 'Failed to fetch users' });
  }
});

// Get user by ID
router.get('/:id', async (req, res) => {
  try {
    const result = await pool.query('SELECT * FROM users WHERE id = $1', [req.params.id]);
    if (result.rows.length === 0) {
      return res.status(404).json({ error: 'User not found' });
    }
    res.json(result.rows[0]);
  } catch (err) {
    console.error('Database error:', err);
    res.status(500).json({ error: 'Failed to fetch user' });
  }
});

// Create user
router.post('/', async (req, res) => {
  const { name, email } = req.body;
  
  if (!name || !email) {
    return res.status(400).json({ error: 'Name and email are required' });
  }

  try {
    const result = await pool.query(
      'INSERT INTO users (name, email) VALUES ($1, $2) RETURNING *',
      [name, email]
    );
    res.status(201).json(result.rows[0]);
  } catch (err) {
    console.error('Database error:', err);
    if (err.code === '23505') {
      return res.status(409).json({ error: 'Email already exists' });
    }
    res.status(500).json({ error: 'Failed to create user' });
  }
});

// Cache test endpoint
router.get('/cache/test', async (req, res) => {
  const key = 'test:timestamp';
  
  try {
    const client = redis.createClient({
      url: process.env.REDIS_URL
    });
    await client.connect();
    
    // Try to get cached value
    let value = await client.get(key);
    
    if (!value) {
      // Set new value
      value = new Date().toISOString();
      await client.setEx(key, 60, value); // Expire in 60 seconds
      var cached = false;
    } else {
      var cached = true;
    }
    
    await client.disconnect();
    
    res.json({
      value,
      cached,
      ttl: cached ? 'expires soon' : 'set for 60s'
    });
  } catch (err) {
    console.error('Redis error:', err);
    res.status(500).json({ error: 'Cache unavailable' });
  }
});

module.exports = router;
