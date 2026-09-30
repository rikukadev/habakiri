class User < ApplicationRecord
  include Indexable
  has_many :posts, dependent: :destroy
  has_many :comments, foreign_key: "author_id"
  has_many :webauthn_credentials, dependent: :destroy
end
