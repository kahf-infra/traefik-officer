// ./test/middleware.js
module.exports = (req, res, next) => {
    // Enable CORS
    res.header('Access-Control-Allow-Origin', '*')
    res.header('Access-Control-Allow-Methods', 'GET, POST, PUT, DELETE, OPTIONS')
    res.header('Access-Control-Allow-Headers', 'Content-Type, Authorization')

    // Continue to JSON Server router
    next()
}