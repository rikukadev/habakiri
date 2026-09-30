class User < ApplicationRecord
  include Indexable
  has_many :posts, dependent: :destroy
  has_many :comments
end
