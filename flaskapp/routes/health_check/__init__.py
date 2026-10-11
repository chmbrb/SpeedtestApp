from flask import Blueprint, request, jsonify


health_check = Blueprint( 'health_check', __name__ )


@health_check.route('/cache-me')
def cache():
	return "nginx will cache this response"

# @health_check.route('/info')
# def info():
# 	resp = {
# 		'connecting_ip': request.headers['X-Real-IP'],
# 		'proxy_ip': request.headers['X-Forwarded-For'],
# 		'host': request.headers['Host'],
# 		'user-agent': request.headers['User-Agent']
# 	}
# 	return jsonify(resp)

@health_check.route('/flask-health-check')
def flask_health_check():
	return "success"

