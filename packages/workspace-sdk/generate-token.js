const jwt = require('jsonwebtoken');

const secret = 'test-token';
const token = jwt.sign(
  { 
    sub: 'test-client',
    iat: Math.floor(Date.now() / 1000),
    exp: Math.floor(Date.now() / 1000) + 3600 
  },
  secret,
  { algorithm: 'HS256' }
);

console.log(token);
