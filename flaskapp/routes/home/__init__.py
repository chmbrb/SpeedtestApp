from flask import Blueprint, flash, render_template, abort, request, redirect, url_for
import json
from jinja2 import TemplateNotFound
# from app.config import Config


# template_folder not required if configured
home = Blueprint( 'home', __name__ )

# @home.route('/', methods=["POST"])
# def index_post():
#     f = request.form
#     user_input = f.get('prompt')
  
@home.route('/', defaults={'page': 'index'}, methods=["GET"])
@home.route('/<page>')
def index(page):
    try:
        return render_template(f'home/{page}.html')
    except TemplateNotFound as ex:
        abort(404)
    except Exception as ex:
        abort(404)

@home.errorhandler(404)
def not_found(err):
    return render_template('404.html')