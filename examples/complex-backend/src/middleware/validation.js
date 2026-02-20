function validateUser(req, res, next) {
  const { name, email, password } = req.body;
  const errors = [];

  if (!name || name.length < 2) {
    errors.push('Name must be at least 2 characters');
  }

  if (!email || !email.includes('@')) {
    errors.push('Valid email is required');
  }

  if (!password || password.length < 8) {
    errors.push('Password must be at least 8 characters');
  }

  if (errors.length > 0) {
    return res.status(400).json({ errors });
  }

  next();
}

function validateProduct(req, res, next) {
  const { name, price } = req.body;
  const errors = [];

  if (!name || name.length < 1) {
    errors.push('Product name is required');
  }

  if (price === undefined || price < 0) {
    errors.push('Valid price is required');
  }

  if (errors.length > 0) {
    return res.status(400).json({ errors });
  }

  next();
}

module.exports = { validateUser, validateProduct };
