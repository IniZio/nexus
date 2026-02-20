const express = require('express');
const router = express.Router();
const db = require('../config/database');
const { authenticate, authorize } = require('../middleware/auth');
const { validateProduct } = require('../middleware/validation');

router.get('/', async (req, res) => {
  try {
    const { category, minPrice, maxPrice, search } = req.query;
    let query = 'SELECT * FROM products WHERE 1=1';
    const params = [];
    let paramIndex = 1;

    if (category) {
      query += ` AND category = $${paramIndex++}`;
      params.push(category);
    }

    if (minPrice !== undefined) {
      query += ` AND price >= $${paramIndex++}`;
      params.push(parseFloat(minPrice));
    }

    if (maxPrice !== undefined) {
      query += ` AND price <= $${paramIndex++}`;
      params.push(parseFloat(maxPrice));
    }

    if (search) {
      query += ` AND (name ILIKE $${paramIndex++} OR description ILIKE $${paramIndex})`;
      params.push(`%${search}%`, `%${search}%`);
    }

    query += ' ORDER BY created_at DESC';
    const result = await db.query(query, params);
    res.json(result.rows);
  } catch (error) {
    res.status(500).json({ error: error.message });
  }
});

router.get('/:id', async (req, res) => {
  try {
    const result = await db.query('SELECT * FROM products WHERE id = $1', [req.params.id]);
    if (result.rows.length === 0) {
      return res.status(404).json({ error: 'Product not found' });
    }
    res.json(result.rows[0]);
  } catch (error) {
    res.status(500).json({ error: error.message });
  }
});

router.post('/', authenticate, authorize(['admin']), validateProduct, async (req, res) => {
  try {
    const { name, description, price, stock_quantity, category } = req.body;
    const result = await db.query(
      'INSERT INTO products (name, description, price, stock_quantity, category) VALUES ($1, $2, $3, $4, $5) RETURNING *',
      [name, description, price, stock_quantity || 0, category]
    );
    res.status(201).json(result.rows[0]);
  } catch (error) {
    res.status(500).json({ error: error.message });
  }
});

router.put('/:id', authenticate, authorize(['admin']), async (req, res) => {
  try {
    const { name, description, price, stock_quantity, category } = req.body;
    const result = await db.query(
      'UPDATE products SET name = COALESCE($1, name), description = COALESCE($2, description), price = COALESCE($3, price), stock_quantity = COALESCE($4, stock_quantity), category = COALESCE($5, category) WHERE id = $6 RETURNING *',
      [name, description, price, stock_quantity, category, req.params.id]
    );
    if (result.rows.length === 0) {
      return res.status(404).json({ error: 'Product not found' });
    }
    res.json(result.rows[0]);
  } catch (error) {
    res.status(500).json({ error: error.message });
  }
});

router.delete('/:id', authenticate, authorize(['admin']), async (req, res) => {
  try {
    const result = await db.query('DELETE FROM products WHERE id = $1 RETURNING id', [req.params.id]);
    if (result.rows.length === 0) {
      return res.status(404).json({ error: 'Product not found' });
    }
    res.status(204).send();
  } catch (error) {
    res.status(500).json({ error: error.message });
  }
});

module.exports = router;
